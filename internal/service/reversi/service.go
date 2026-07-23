// The reversi service: game.Table lifecycle, on-demand Start, computed
// passes (never sent), applyAndHash convention.
package reversi

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	rvengine "github.com/richardwooding/reversi"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// The reversi rules engine lives in its own module
// (github.com/richardwooding/reversi); re-export the API this service uses.
type Board = rvengine.Board

var (
	Start           = rvengine.Start
	LegalSquares    = rvengine.LegalSquares
	Place           = rvengine.Place
	Advance         = rvengine.Advance
	Counts          = rvengine.Counts
	ErrIllegalPlace = rvengine.ErrIllegalPlace
)

const ID = "reversi"

const (
	kindNewGame  uint8 = 1
	kindStartReq uint8 = 2
	kindPlace    uint8 = 3
	kindResign   uint8 = 4
)

type msg struct {
	Kind      uint8  `cbor:"1,keyasint"`
	P1        uint32 `cbor:"2,keyasint,omitempty"`
	P2        uint32 `cbor:"3,keyasint,omitempty"`
	Sq        int8   `cbor:"4,keyasint,omitempty"`
	StateHash []byte `cbor:"5,keyasint,omitempty"`
}

type snapshot struct {
	Board  Board  `cbor:"1,keyasint"`
	P1     uint32 `cbor:"2,keyasint"`
	P2     uint32 `cbor:"3,keyasint"`
	Turn   int8   `cbor:"4,keyasint"` // +1 / -1
	Phase  uint8  `cbor:"5,keyasint"`
	Winner int8   `cbor:"6,keyasint"` // 0 undecided, +1/-1 side, 2 draw, 3/-3 forfeit win
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing bool
	Board   Board
	P1ID    wire.ParticipantID // black, moves first
	P2ID    wire.ParticipantID // white
	TurnID  wire.ParticipantID
	Legal   []int8 // only populated for the mover
	Outcome string // "", "black wins 40-24", "draw 32-32", …
	Passed  bool   // the last advance skipped the opponent
	Black   int
	White   int
	LastSq  int8 // -1 when none
}

// Service implements service.Service.
type Service struct {
	ctx service.Context

	mu     sync.Mutex
	table  game.Table
	board  Board
	ph     game.Phase
	turn   int8 // +1 black, -1 white
	winner int8 // 0 undecided; +1/-1 by discs; 2 draw; +3/-3 forfeit/resign
	passed bool
	lastSq int8
}

func New() *Service { return &Service{lastSq: -1} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.ctx = ctx }

func (s *Service) MemberKeyed(id wire.ParticipantID, role session.Role) {
	if !s.ctx.Host {
		return
	}
	s.mu.Lock()
	s.table.NoteKeyed(id, role)
	s.mu.Unlock()
}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	winner, forfeit := s.table.NoteLeft(id, s.ph)
	if forfeit {
		s.winner = forfeitWinner(winner)
		s.ph = game.Over
	}
	s.mu.Unlock()
	if forfeit {
		s.emitState()
	}
}

// forfeitWinner maps a seat side to the forfeit-winner marker (+3 black won,
// -3 white won).
func forfeitWinner(side game.Side) int8 {
	if side == game.P1 {
		return 3
	}
	return -3
}

func (s *Service) Start() error {
	if !s.ctx.Host {
		body, err := wire.Marshal(msg{Kind: kindStartReq})
		if err != nil {
			return err
		}
		return s.ctx.Send.SendTo(s.ctx.HostID, ID, body)
	}
	return s.hostStart(s.ctx.Self)
}

func (s *Service) hostStart(from wire.ParticipantID) error {
	s.mu.Lock()
	if err := s.table.AuthorizeStart(s.ctx.Host, from, s.ctx.Self, s.ph); err != nil {
		s.mu.Unlock()
		return err
	}
	seats := s.table.NextSeats(s.ctx.Self)
	s.resetLocked(seats)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindNewGame, P1: uint32(seats.P1), P2: uint32(seats.P2)})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) resetLocked(seats game.Seats) {
	s.board = Start()
	s.table.Seats = seats
	s.ph = game.Playing
	s.turn = 1
	s.winner = 0
	s.passed = false
	s.lastSq = -1
}

// PlaceDisc plays the local player's placement.
func (s *Service) PlaceDisc(sq int8) error {
	s.mu.Lock()
	if err := s.checkTurnLocked(s.ctx.Self); err != nil {
		s.mu.Unlock()
		return err
	}
	nb, err := Place(s.board, s.turn, sq)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.board = nb
	s.lastSq = sq
	hash := s.applyAndHashLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindPlace, Sq: sq, StateHash: hash})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// Resign concedes.
func (s *Service) Resign() error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(s.ctx.Self)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("reversi: no game to resign")
	}
	s.winner = forfeitWinner(side.Opponent())
	s.ph = game.Over
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

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("reversi: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		if from != s.ctx.HostID {
			return fmt.Errorf("reversi: new game from non-host %d", from)
		}
		s.mu.Lock()
		s.resetLocked(game.Seats{P1: wire.ParticipantID(m.P1), P2: wire.ParticipantID(m.P2)})
		s.mu.Unlock()
		s.emitState()
		return nil
	case kindStartReq:
		if !s.ctx.Host {
			return nil
		}
		return s.hostStart(from)
	case kindPlace:
		return s.handlePlace(from, m)
	case kindResign:
		return s.handleResign(from)
	}
	return fmt.Errorf("reversi: unknown message kind %d", m.Kind)
}

func (s *Service) handlePlace(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	if err := s.checkTurnLocked(from); err != nil {
		s.mu.Unlock()
		return err
	}
	nb, err := Place(s.board, s.turn, m.Sq)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.board = nb
	s.lastSq = m.Sq
	hash := s.applyAndHashLocked()
	ok := bytes.Equal(hash, m.StateHash)
	if !ok {
		s.ph = game.Over
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("reversi: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("reversi: resign outside game")
	}
	s.winner = forfeitWinner(side.Opponent())
	s.ph = game.Over
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ph == game.Idle {
		return nil, nil
	}
	return wire.Marshal(snapshot{
		Board: s.board, P1: uint32(s.table.Seats.P1), P2: uint32(s.table.Seats.P2),
		Turn: s.turn, Phase: uint8(s.ph), Winner: s.winner,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("reversi: restore: %w", err)
	}
	s.mu.Lock()
	// Late-joiner catch-up only (see chess/backgammon for why).
	if s.ph != game.Idle {
		s.mu.Unlock()
		return nil
	}
	s.board = snap.Board
	s.table.Seats = game.Seats{P1: wire.ParticipantID(snap.P1), P2: wire.ParticipantID(snap.P2)}
	s.turn = snap.Turn
	s.ph = game.Phase(snap.Phase)
	s.winner = snap.Winner
	s.lastSq = -1
	s.mu.Unlock()
	s.emitState()
	return nil
}

// State returns the current state for UI pulls.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

// --- internals ---------------------------------------------------------------

func (s *Service) checkTurnLocked(who wire.ParticipantID) error {
	if s.ph != game.Playing {
		return errors.New("reversi: no game in progress")
	}
	side, seated := s.table.Seats.SideOf(who)
	if !seated {
		return errors.New("reversi: not a player")
	}
	moverSide := game.P1
	if s.turn == -1 {
		moverSide = game.P2
	}
	if side != moverSide {
		return errors.New("reversi: not your turn")
	}
	return nil
}

// applyAndHashLocked advances via the engine (computed passes) and hashes
// the post-advance state — the shared M3 convention.
func (s *Service) applyAndHashLocked() []byte {
	next, passed, over := Advance(s.board, s.turn)
	s.passed = passed
	if over {
		black, white := Counts(s.board)
		switch {
		case black > white:
			s.winner = 1
		case white > black:
			s.winner = -1
		default:
			s.winner = 2
		}
		s.ph = game.Over
	} else {
		s.turn = next
	}
	b, err := wire.Marshal(struct {
		Board Board `cbor:"1,keyasint"`
		Turn  int8  `cbor:"2,keyasint"`
		Phase uint8 `cbor:"3,keyasint"`
	}{s.board, s.turn, uint8(s.ph)})
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(b)
	return sum[:8]
}

func (s *Service) emitState() {
	s.mu.Lock()
	st := s.stateLocked()
	s.mu.Unlock()
	s.ctx.Emit(st)
}

func (s *Service) stateLocked() State {
	if s.ph == game.Idle {
		return State{LastSq: -1}
	}
	black, white := Counts(s.board)
	st := State{
		Playing: true,
		Board:   s.board,
		P1ID:    s.table.Seats.P1,
		P2ID:    s.table.Seats.P2,
		Passed:  s.passed,
		Black:   black,
		White:   white,
		LastSq:  s.lastSq,
	}
	if s.ph == game.Over {
		st.Outcome = outcomeText(s.winner, black, white)
		return st
	}
	moverSide := game.P1
	if s.turn == -1 {
		moverSide = game.P2
	}
	st.TurnID = s.table.Seats.IDOf(moverSide)
	if st.TurnID == s.ctx.Self {
		st.Legal = LegalSquares(s.board, s.turn)
	}
	return st
}

func outcomeText(winner int8, black, white int) string {
	switch winner {
	case 1:
		return fmt.Sprintf("black wins %d-%d", black, white)
	case -1:
		return fmt.Sprintf("white wins %d-%d", white, black)
	case 2:
		return fmt.Sprintf("draw %d-%d", black, white)
	case 3:
		return "black wins (forfeit)"
	case -3:
		return "white wins (forfeit)"
	}
	return ""
}
