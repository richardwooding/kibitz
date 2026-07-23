// Package chess is the first game service, wrapping notnil/chess for rules.
// Sync is both-sides-validate: every client applies every move through the
// same engine and checks a position hash — there is no authoritative server,
// because the relay can't be one (it only ever sees ciphertext).
package chess

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	notnil "github.com/notnil/chess"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

const ID = "chess"

const (
	kindMove      uint8 = 1
	kindResign    uint8 = 2
	kindOfferDraw uint8 = 3
	kindAgreeDraw uint8 = 4
	kindNewGame   uint8 = 5
)

type msg struct {
	Kind uint8 `cbor:"1,keyasint"`
	// UCI move (e2e4, e7e8q) for kindMove.
	UCI string `cbor:"2,keyasint,omitempty"`
	// StateHash is SHA-256(FEN)[:8] AFTER the move — the desync tripwire.
	StateHash []byte `cbor:"3,keyasint,omitempty"`
	// WhiteID for kindNewGame.
	WhiteID uint32 `cbor:"4,keyasint,omitempty"`
	BlackID uint32 `cbor:"5,keyasint,omitempty"`
}

type snapshot struct {
	PGN     string `cbor:"1,keyasint"`
	WhiteID uint32 `cbor:"2,keyasint"`
	BlackID uint32 `cbor:"3,keyasint"`
}

// State is emitted after every game change; the UI renders it directly.
type State struct {
	FEN     string
	WhiteID wire.ParticipantID
	BlackID wire.ParticipantID
	TurnID  wire.ParticipantID // 0 when the game is over or not started
	Outcome string             // "*", "1-0", "0-1", "1/2-1/2"
	Method  string             // "Checkmate", "Resignation", …
	LastUCI string
	Playing bool // a game exists (start conditions met)
}

// DrawOffered is emitted when the opponent offers a draw.
type DrawOffered struct{ From wire.ParticipantID }

// Desync is emitted when a peer's move or state hash disagrees with the
// local engine — the game cannot safely continue.
type Desync struct {
	From   wire.ParticipantID
	Reason string
}

var (
	ErrNotPlayer = errors.New("chess: you are not a player in this game")
	ErrNotTurn   = errors.New("chess: not your turn")
	ErrNoGame    = errors.New("chess: no game in progress")
)

// Service implements service.Service. HandleFrame/Snapshot/Restore run on
// the mux goroutine; TryMove/Resign/OfferDraw/LegalTargets come from the UI
// layer — the mutex covers game state.
type Service struct {
	ctx service.Context

	mu        sync.Mutex
	game      *notnil.Game
	whiteID   wire.ParticipantID
	blackID   wire.ParticipantID
	lastUCI   string
	drawnFrom wire.ParticipantID // pending draw offer
}

func New() *Service { return &Service{} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.ctx = ctx }

// MemberKeyed (host side): the first player to join starts the game — host
// takes white for MVP (color choice is a UI nicety for later).
func (s *Service) MemberKeyed(id wire.ParticipantID, role session.Role) {
	if !s.ctx.Host || role != session.RolePlayer {
		return
	}
	s.mu.Lock()
	if s.game != nil {
		s.mu.Unlock()
		return
	}
	s.game = notnil.NewGame()
	s.whiteID = s.ctx.Self
	s.blackID = id
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindNewGame, WhiteID: uint32(s.ctx.Self), BlackID: uint32(id)})
	if err == nil {
		_ = s.ctx.Send.Broadcast(ID, body)
	}
	s.emitState()
}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	abandoned := s.game != nil && s.game.Outcome() == notnil.NoOutcome &&
		(id == s.whiteID || id == s.blackID)
	if abandoned {
		// Opponent walked away mid-game: they forfeit.
		if id == s.whiteID {
			s.game.Resign(notnil.White)
		} else {
			s.game.Resign(notnil.Black)
		}
	}
	s.mu.Unlock()
	if abandoned {
		s.emitState()
	}
}

// TryMove validates and broadcasts the local player's move (UCI: e2e4, e7e8q).
func (s *Service) TryMove(uci string) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if err := s.checkTurnLocked(s.ctx.Self); err != nil {
		s.mu.Unlock()
		return err
	}
	move, err := notnil.UCINotation{}.Decode(s.game.Position(), uci)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("chess: bad move %q: %w", uci, err)
	}
	if err := s.game.Move(move); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("chess: illegal move %q: %w", uci, err)
	}
	s.lastUCI = uci
	s.drawnFrom = 0
	hash := positionHash(s.game)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindMove, UCI: uci, StateHash: hash})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// Resign concedes the local player's game.
func (s *Service) Resign() error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	color, err := s.colorOfLocked(s.ctx.Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.game.Resign(color)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindResign})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// OfferDraw proposes a draw; AgreeDraw accepts a pending offer.
func (s *Service) OfferDraw() error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if _, err := s.colorOfLocked(s.ctx.Self); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindOfferDraw})
	if err != nil {
		return err
	}
	return s.ctx.Send.Broadcast(ID, body)
}

func (s *Service) AgreeDraw() error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if s.drawnFrom == 0 {
		s.mu.Unlock()
		return errors.New("chess: no draw offer pending")
	}
	if _, err := s.colorOfLocked(s.ctx.Self); err != nil {
		s.mu.Unlock()
		return err
	}
	_ = s.game.Draw(notnil.DrawOffer)
	s.drawnFrom = 0
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindAgreeDraw})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// LegalTargets returns destination squares for the piece on from ("e2") —
// the UI's move-highlighting query.
func (s *Service) LegalTargets(from string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.game == nil {
		return nil
	}
	var out []string
	for _, m := range s.game.ValidMoves() {
		if m.S1().String() == from {
			out = append(out, m.S2().String())
		}
	}
	return out
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("chess: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		return s.handleNewGame(from, m)
	case kindMove:
		return s.handleMove(from, m)
	case kindResign:
		return s.handleResign(from)
	case kindOfferDraw:
		return s.handleOfferDraw(from)
	case kindAgreeDraw:
		return s.handleAgreeDraw(from)
	}
	return fmt.Errorf("chess: unknown message kind %d", m.Kind)
}

func (s *Service) handleNewGame(from wire.ParticipantID, m msg) error {
	if from != s.ctx.HostID {
		return fmt.Errorf("chess: new game from non-host %d", from)
	}
	s.mu.Lock()
	s.game = notnil.NewGame()
	s.whiteID = wire.ParticipantID(m.WhiteID)
	s.blackID = wire.ParticipantID(m.BlackID)
	s.lastUCI = ""
	s.drawnFrom = 0
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleMove(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if err := s.checkTurnLocked(from); err != nil {
		s.mu.Unlock()
		s.ctx.Emit(Desync{From: from, Reason: "move out of turn"})
		return err
	}
	move, err := notnil.UCINotation{}.Decode(s.game.Position(), m.UCI)
	if err == nil {
		err = s.game.Move(move)
	}
	if err != nil {
		s.mu.Unlock()
		s.ctx.Emit(Desync{From: from, Reason: fmt.Sprintf("illegal move %s", m.UCI)})
		return fmt.Errorf("chess: peer sent illegal move %q: %w", m.UCI, err)
	}
	s.lastUCI = m.UCI
	s.drawnFrom = 0
	hash := positionHash(s.game)
	s.mu.Unlock()

	if !bytes.Equal(hash, m.StateHash) {
		s.ctx.Emit(Desync{From: from, Reason: "position hash mismatch"})
		return errors.New("chess: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	color, err := s.colorOfLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.game.Resign(color)
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleOfferDraw(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if _, err := s.colorOfLocked(from); err != nil {
		s.mu.Unlock()
		return err
	}
	s.drawnFrom = from
	s.mu.Unlock()
	s.ctx.Emit(DrawOffered{From: from})
	return nil
}

func (s *Service) handleAgreeDraw(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if _, err := s.colorOfLocked(from); err != nil {
		s.mu.Unlock()
		return err
	}
	_ = s.game.Draw(notnil.DrawOffer)
	s.drawnFrom = 0
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.game == nil {
		return nil, nil
	}
	return wire.Marshal(snapshot{
		PGN:     s.game.String(),
		WhiteID: uint32(s.whiteID),
		BlackID: uint32(s.blackID),
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("chess: restore: %w", err)
	}
	game := notnil.NewGame()
	if err := game.UnmarshalText([]byte(snap.PGN)); err != nil {
		return fmt.Errorf("chess: restore PGN: %w", err)
	}
	s.mu.Lock()
	// Late-joiner catch-up only: a client with a live game saw everything
	// in the snapshot already (and may have moved since the host built it).
	if s.game != nil {
		s.mu.Unlock()
		return nil
	}
	s.game = game
	s.whiteID = wire.ParticipantID(snap.WhiteID)
	s.blackID = wire.ParticipantID(snap.BlackID)
	s.mu.Unlock()
	s.emitState()
	return nil
}

// State returns the current game state (for UI pulls; pushes come via Emit).
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Service) emitState() {
	s.mu.Lock()
	st := s.stateLocked()
	s.mu.Unlock()
	s.ctx.Emit(st)
}

func (s *Service) stateLocked() State {
	if s.game == nil {
		return State{}
	}
	st := State{
		FEN:     s.game.FEN(),
		WhiteID: s.whiteID,
		BlackID: s.blackID,
		Outcome: s.game.Outcome().String(),
		Method:  s.game.Method().String(),
		LastUCI: s.lastUCI,
		Playing: true,
	}
	if s.game.Outcome() == notnil.NoOutcome {
		if s.game.Position().Turn() == notnil.White {
			st.TurnID = s.whiteID
		} else {
			st.TurnID = s.blackID
		}
	}
	return st
}

func (s *Service) checkTurnLocked(who wire.ParticipantID) error {
	if s.game.Outcome() != notnil.NoOutcome {
		return errors.New("chess: game is over")
	}
	color, err := s.colorOfLocked(who)
	if err != nil {
		return err
	}
	if s.game.Position().Turn() != color {
		return ErrNotTurn
	}
	return nil
}

func (s *Service) colorOfLocked(who wire.ParticipantID) (notnil.Color, error) {
	switch who {
	case s.whiteID:
		return notnil.White, nil
	case s.blackID:
		return notnil.Black, nil
	default:
		return notnil.NoColor, ErrNotPlayer
	}
}

func positionHash(g *notnil.Game) []byte {
	sum := sha256.Sum256([]byte(g.FEN()))
	return sum[:8]
}
