// The backgammon service: engine + fair dice + turn protocol. Like chess,
// sync is both-sides-validate — every participant (spectators included)
// applies every turn through the same engine, verifies every dice roll's
// commit-reveal exchange, and checks a position hash after every turn.
//
// A roll is one user action (Roll) plus two automatic messages:
//
//	roller: rollCommit ──▶ opponent: rollResponse ──▶ roller: rollReveal
//
// The relay's hub serializes broadcasts, so every participant observes the
// exchange in the same order. Dances (no legal moves) auto-submit the empty
// turn. The opening roll runs automatically when the game starts; the higher
// die's owner moves first playing both opening dice. Doubling cube: not yet.
package backgammon

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/richardwooding/kibitz/internal/fairdice"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

const ID = "backgammon"

const (
	kindNewGame      uint8 = 1
	kindRollCommit   uint8 = 2
	kindRollResponse uint8 = 3
	kindRollReveal   uint8 = 4
	kindTurn         uint8 = 5
	kindResign       uint8 = 6
)

type msg struct {
	Kind      uint8            `cbor:"1,keyasint"`
	WhiteID   uint32           `cbor:"2,keyasint,omitempty"`
	BlackID   uint32           `cbor:"3,keyasint,omitempty"`
	Commit    []byte           `cbor:"4,keyasint,omitempty"`
	RB        []byte           `cbor:"5,keyasint,omitempty"`
	Reveal    *fairdice.Reveal `cbor:"6,keyasint,omitempty"`
	Hops      []Hop            `cbor:"7,keyasint,omitempty"`
	StateHash []byte           `cbor:"8,keyasint,omitempty"`
}

type snapshot struct {
	Board     Board   `cbor:"1,keyasint"`
	WhiteID   uint32  `cbor:"2,keyasint"`
	BlackID   uint32  `cbor:"3,keyasint"`
	TurnColor Color   `cbor:"4,keyasint"`
	Phase     uint8   `cbor:"5,keyasint"`
	Dice      [2]int8 `cbor:"6,keyasint"`
	Winner    int8    `cbor:"7,keyasint"` // -1 none, else Color
	Points    int8    `cbor:"8,keyasint"`
	// In-flight roll exchange, so a late joiner can process a reveal it
	// didn't see the start of (the snapshotting host is trusted by design).
	Opening      bool   `cbor:"9,keyasint,omitempty"`
	HaveCommit   bool   `cbor:"10,keyasint,omitempty"`
	Commit       []byte `cbor:"11,keyasint,omitempty"`
	HaveResponse bool   `cbor:"12,keyasint,omitempty"`
	RB           []byte `cbor:"13,keyasint,omitempty"`
}

type phase uint8

const (
	phaseNone      phase = 0 // no game yet
	phaseRoll      phase = 1 // waiting for the mover to click Roll
	phaseHandshake phase = 2 // commit/response/reveal in flight
	phaseMove      phase = 3 // dice known, waiting for the mover's turn
	phaseOver      phase = 4
)

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing bool
	Board   Board
	WhiteID wire.ParticipantID
	BlackID wire.ParticipantID
	TurnID  wire.ParticipantID // who must act (roll or move); 0 when over
	Phase   string             // "rolling" | "handshake" | "moving" | "over"
	Dice    [2]int8            // valid in "moving"
	Legal   [][]Hop            // legal turns, only populated for the mover
	Outcome string             // "", or "white wins (gammon, 2pts)" etc.
	PipsW   int
	PipsB   int
}

// Danced is emitted when a player had no legal moves and the turn passed.
type Danced struct{ By wire.ParticipantID }

// CheatDetected: a reveal didn't match its commitment. The game freezes —
// there is no honest continuation.
type CheatDetected struct{ By wire.ParticipantID }

var (
	ErrNotPlayer = errors.New("backgammon: you are not a player in this game")
	ErrNotTurn   = errors.New("backgammon: not your turn")
	ErrPhase     = errors.New("backgammon: action not valid in this phase")
)

// Service implements service.Service. HandleFrame/Snapshot/Restore run on
// the mux goroutine; Roll/Move/Resign come from the UI — the mutex covers
// all game state.
type Service struct {
	ctx service.Context

	mu        sync.Mutex
	board     Board
	whiteID   wire.ParticipantID
	blackID   wire.ParticipantID
	turnColor Color
	ph        phase
	opening   bool
	dice      [2]int8
	result    *Result

	// roll exchange state
	myReveal     *fairdice.Reveal
	rollCommit   fairdice.Commit
	haveCommit   bool
	rollResponse fairdice.Response
	haveResponse bool
}

func New() *Service { return &Service{} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.ctx = ctx }

// MemberKeyed (host side): first player joining starts the game. Host plays
// white; the opening roll decides who MOVES first.
func (s *Service) MemberKeyed(id wire.ParticipantID, role session.Role) {
	if !s.ctx.Host || role != session.RolePlayer {
		return
	}
	s.mu.Lock()
	if s.ph != phaseNone {
		s.mu.Unlock()
		return
	}
	s.board = Start()
	s.whiteID = s.ctx.Self
	s.blackID = id
	s.ph = phaseHandshake
	s.opening = true
	s.mu.Unlock()

	if body, err := wire.Marshal(msg{Kind: kindNewGame, WhiteID: uint32(s.ctx.Self), BlackID: uint32(id)}); err == nil {
		_ = s.ctx.Send.Broadcast(ID, body)
	}
	// The host is the opening roller; the exchange runs automatically.
	s.sendCommit()
}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	forfeit := s.ph != phaseNone && s.ph != phaseOver && (id == s.whiteID || id == s.blackID)
	if forfeit {
		winner := White
		if id == s.whiteID {
			winner = Black
		}
		s.result = &Result{Winner: winner, Points: 1}
		s.ph = phaseOver
	}
	s.mu.Unlock()
	if forfeit {
		s.emitState()
	}
}

// Roll is the mover's explicit action to start their dice exchange.
func (s *Service) Roll() error {
	s.mu.Lock()
	if s.ph != phaseRoll {
		s.mu.Unlock()
		return ErrPhase
	}
	if s.playerIDLocked(s.turnColor) != s.ctx.Self {
		s.mu.Unlock()
		return ErrNotTurn
	}
	s.ph = phaseHandshake
	s.mu.Unlock()
	s.sendCommit()
	s.emitState()
	return nil
}

// Move submits the mover's complete turn.
func (s *Service) Move(hops []Hop) error {
	s.mu.Lock()
	if s.ph != phaseMove {
		s.mu.Unlock()
		return ErrPhase
	}
	color := s.turnColor
	if s.playerIDLocked(color) != s.ctx.Self {
		s.mu.Unlock()
		return ErrNotTurn
	}
	if err := Validate(s.board, color, s.dice[0], s.dice[1], hops); err != nil {
		s.mu.Unlock()
		return err
	}
	s.board = ApplyTurn(s.board, color, hops)
	hash := s.stateHashLocked()
	s.advanceLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindTurn, Hops: hops, StateHash: hash})
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
	if s.ph == phaseNone || s.ph == phaseOver {
		s.mu.Unlock()
		return ErrPhase
	}
	var loser Color
	switch s.ctx.Self {
	case s.whiteID:
		loser = White
	case s.blackID:
		loser = Black
	default:
		s.mu.Unlock()
		return ErrNotPlayer
	}
	s.result = &Result{Winner: loser.Opponent(), Points: 1}
	s.ph = phaseOver
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

// State returns the current game state for UI pulls.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("backgammon: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		return s.handleNewGame(from, m)
	case kindRollCommit:
		return s.handleRollCommit(from, m)
	case kindRollResponse:
		return s.handleRollResponse(from, m)
	case kindRollReveal:
		return s.handleRollReveal(from, m)
	case kindTurn:
		return s.handleTurn(from, m)
	case kindResign:
		return s.handleResign(from)
	}
	return fmt.Errorf("backgammon: unknown message kind %d", m.Kind)
}

func (s *Service) handleNewGame(from wire.ParticipantID, m msg) error {
	if from != s.ctx.HostID {
		return fmt.Errorf("backgammon: new game from non-host %d", from)
	}
	s.mu.Lock()
	s.board = Start()
	s.whiteID = wire.ParticipantID(m.WhiteID)
	s.blackID = wire.ParticipantID(m.BlackID)
	s.ph = phaseHandshake
	s.opening = true
	s.result = nil
	s.haveCommit, s.haveResponse, s.myReveal = false, false, nil
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleRollCommit(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	roller := s.rollerIDLocked()
	if s.ph != phaseHandshake && s.ph != phaseRoll {
		s.mu.Unlock()
		return fmt.Errorf("backgammon: unexpected commit in phase %d", s.ph)
	}
	if from != roller {
		s.mu.Unlock()
		return fmt.Errorf("backgammon: commit from %d, expected roller %d", from, roller)
	}
	if len(m.Commit) != len(s.rollCommit) {
		s.mu.Unlock()
		return errors.New("backgammon: malformed commit")
	}
	copy(s.rollCommit[:], m.Commit)
	s.haveCommit = true
	s.ph = phaseHandshake
	iRespond := s.opponentIDLocked() == s.ctx.Self
	s.mu.Unlock()

	// The other player answers automatically.
	if iRespond {
		rb, err := fairdice.NewResponse()
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.rollResponse = rb
		s.haveResponse = true
		s.mu.Unlock()
		body, err := wire.Marshal(msg{Kind: kindRollResponse, RB: rb[:]})
		if err != nil {
			return err
		}
		return s.ctx.Send.Broadcast(ID, body)
	}
	return nil
}

func (s *Service) handleRollResponse(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	if s.ph != phaseHandshake || !s.haveCommit {
		s.mu.Unlock()
		return errors.New("backgammon: unexpected roll response")
	}
	if from != s.opponentIDLocked() {
		s.mu.Unlock()
		return fmt.Errorf("backgammon: response from %d, expected %d", from, s.opponentIDLocked())
	}
	if len(m.RB) != len(s.rollResponse) {
		s.mu.Unlock()
		return errors.New("backgammon: malformed response")
	}
	copy(s.rollResponse[:], m.RB)
	s.haveResponse = true
	iReveal := s.myReveal != nil && s.rollerIDLocked() == s.ctx.Self
	var reveal fairdice.Reveal
	if iReveal {
		reveal = *s.myReveal
	}
	s.mu.Unlock()

	// The roller discloses automatically.
	if iReveal {
		body, err := wire.Marshal(msg{Kind: kindRollReveal, Reveal: &reveal})
		if err != nil {
			return err
		}
		if err := s.ctx.Send.Broadcast(ID, body); err != nil {
			return err
		}
		return s.applyReveal(s.ctx.Self, reveal)
	}
	return nil
}

func (s *Service) handleRollReveal(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	roller := s.rollerIDLocked()
	ok := s.ph == phaseHandshake && s.haveCommit && s.haveResponse && from == roller && m.Reveal != nil
	s.mu.Unlock()
	if !ok {
		return errors.New("backgammon: unexpected reveal")
	}
	return s.applyReveal(from, *m.Reveal)
}

// applyReveal verifies the exchange and turns it into dice for everyone.
func (s *Service) applyReveal(roller wire.ParticipantID, reveal fairdice.Reveal) error {
	s.mu.Lock()
	if !fairdice.Verify(s.rollCommit, reveal) {
		s.ph = phaseOver
		s.mu.Unlock()
		s.ctx.Emit(CheatDetected{By: roller})
		return fmt.Errorf("backgammon: reveal does not match commitment from %d", roller)
	}
	if s.opening {
		hostDie, otherDie := fairdice.Opening(reveal, s.rollResponse)
		// The roller of the opening is always white (the host).
		if hostDie > otherDie {
			s.turnColor = White
		} else {
			s.turnColor = Black
		}
		s.dice = [2]int8{int8(hostDie), int8(otherDie)}
		s.opening = false
	} else {
		d1, d2 := fairdice.Dice(reveal, s.rollResponse)
		s.dice = [2]int8{int8(d1), int8(d2)}
	}
	s.ph = phaseMove
	s.haveCommit, s.haveResponse, s.myReveal = false, false, nil

	// Dance: the mover has no legal moves — auto-pass so nobody sits on a
	// dead turn.
	moverIsMe := s.playerIDLocked(s.turnColor) == s.ctx.Self
	turns := LegalTurns(s.board, s.turnColor, s.dice[0], s.dice[1])
	dance := len(turns) == 1 && len(turns[0]) == 0
	var hash []byte
	if dance && moverIsMe {
		// Hash BEFORE advancing: receivers hash with turn = the mover,
		// exactly as handleTurn does for a normal turn.
		hash = s.stateHashLocked()
		s.advanceLocked()
	}
	s.mu.Unlock()

	if dance && moverIsMe {
		s.ctx.Emit(Danced{By: s.ctx.Self})
		body, err := wire.Marshal(msg{Kind: kindTurn, Hops: nil, StateHash: hash})
		if err != nil {
			return err
		}
		if err := s.ctx.Send.Broadcast(ID, body); err != nil {
			return err
		}
	}
	s.emitState()
	return nil
}

func (s *Service) handleTurn(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	if s.ph != phaseMove {
		s.mu.Unlock()
		return errors.New("backgammon: turn outside moving phase")
	}
	color := s.turnColor
	if from != s.playerIDLocked(color) {
		s.mu.Unlock()
		return fmt.Errorf("backgammon: turn from %d out of turn", from)
	}
	if err := Validate(s.board, color, s.dice[0], s.dice[1], m.Hops); err != nil {
		s.mu.Unlock()
		return err
	}
	s.board = ApplyTurn(s.board, color, m.Hops)
	hash := s.stateHashLocked()
	if !bytes.Equal(hash, m.StateHash) {
		s.ph = phaseOver
		s.mu.Unlock()
		return errors.New("backgammon: position hash mismatch")
	}
	danced := len(m.Hops) == 0
	s.advanceLocked()
	s.mu.Unlock()

	if danced {
		s.ctx.Emit(Danced{By: from})
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.ph == phaseNone || s.ph == phaseOver {
		s.mu.Unlock()
		return errors.New("backgammon: resign outside game")
	}
	var loser Color
	switch from {
	case s.whiteID:
		loser = White
	case s.blackID:
		loser = Black
	default:
		s.mu.Unlock()
		return ErrNotPlayer
	}
	s.result = &Result{Winner: loser.Opponent(), Points: 1}
	s.ph = phaseOver
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ph == phaseNone {
		return nil, nil
	}
	snap := snapshot{
		Board:     s.board,
		WhiteID:   uint32(s.whiteID),
		BlackID:   uint32(s.blackID),
		TurnColor: s.turnColor,
		Phase:     uint8(s.ph),
		Dice:      s.dice,
		Winner:    -1,
		Opening:   s.opening,
	}
	// Carry the in-flight roll exchange so the joiner can finish it.
	if s.ph == phaseHandshake {
		snap.HaveCommit = s.haveCommit
		if s.haveCommit {
			snap.Commit = append([]byte{}, s.rollCommit[:]...)
		}
		snap.HaveResponse = s.haveResponse
		if s.haveResponse {
			snap.RB = append([]byte{}, s.rollResponse[:]...)
		}
	}
	if s.result != nil {
		snap.Winner = int8(s.result.Winner)
		snap.Points = int8(s.result.Points)
	}
	return wire.Marshal(snap)
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("backgammon: restore: %w", err)
	}
	s.mu.Lock()
	// Snapshots catch up LATE joiners. A client that already has a live
	// game saw everything the snapshot describes — and restoring would
	// wipe state the host didn't know yet (e.g. our own in-flight roll
	// response, which is never re-delivered).
	if s.ph != phaseNone {
		s.mu.Unlock()
		return nil
	}
	s.board = snap.Board
	s.whiteID = wire.ParticipantID(snap.WhiteID)
	s.blackID = wire.ParticipantID(snap.BlackID)
	s.turnColor = snap.TurnColor
	s.ph = phase(snap.Phase)
	s.dice = snap.Dice
	s.opening = snap.Opening
	s.haveCommit = snap.HaveCommit
	if snap.HaveCommit && len(snap.Commit) == len(s.rollCommit) {
		copy(s.rollCommit[:], snap.Commit)
	}
	s.haveResponse = snap.HaveResponse
	if snap.HaveResponse && len(snap.RB) == len(s.rollResponse) {
		copy(s.rollResponse[:], snap.RB)
	}
	if snap.Winner >= 0 {
		s.result = &Result{Winner: Color(snap.Winner), Points: int(snap.Points)}
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

// --- internals (locked helpers) ---------------------------------------------

// advanceLocked runs after a turn is applied: winner check or pass the roll.
func (s *Service) advanceLocked() {
	if r := s.board.Winner(); r != nil {
		s.result = r
		s.ph = phaseOver
		return
	}
	s.turnColor = s.turnColor.Opponent()
	s.ph = phaseRoll
}

func (s *Service) playerIDLocked(c Color) wire.ParticipantID {
	if c == White {
		return s.whiteID
	}
	return s.blackID
}

// rollerIDLocked: who is (or would be) the roller of the current exchange.
// During the opening that's always white/host.
func (s *Service) rollerIDLocked() wire.ParticipantID {
	if s.opening {
		return s.whiteID
	}
	return s.playerIDLocked(s.turnColor)
}

// opponentIDLocked: the player who responds to the current roller.
func (s *Service) opponentIDLocked() wire.ParticipantID {
	if s.rollerIDLocked() == s.whiteID {
		return s.blackID
	}
	return s.whiteID
}

func (s *Service) sendCommit() {
	reveal, commit, err := fairdice.NewRoll()
	if err != nil {
		return
	}
	s.mu.Lock()
	s.myReveal = &reveal
	s.rollCommit = commit
	s.haveCommit = true
	s.mu.Unlock()
	if body, err := wire.Marshal(msg{Kind: kindRollCommit, Commit: commit[:]}); err == nil {
		_ = s.ctx.Send.Broadcast(ID, body)
	}
}

func (s *Service) stateHashLocked() []byte {
	b, err := wire.Marshal(struct {
		Board Board `cbor:"1,keyasint"`
		Turn  Color `cbor:"2,keyasint"`
	}{s.board, s.turnColor})
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
	if s.ph == phaseNone {
		return State{}
	}
	st := State{
		Playing: true,
		Board:   s.board,
		WhiteID: s.whiteID,
		BlackID: s.blackID,
		Dice:    s.dice,
		PipsW:   s.board.PipCount(White),
		PipsB:   s.board.PipCount(Black),
	}
	switch s.ph {
	case phaseRoll:
		st.Phase = "rolling"
		st.TurnID = s.playerIDLocked(s.turnColor)
	case phaseHandshake:
		st.Phase = "handshake"
		st.TurnID = s.rollerIDLocked()
	case phaseMove:
		st.Phase = "moving"
		st.TurnID = s.playerIDLocked(s.turnColor)
		if st.TurnID == s.ctx.Self {
			st.Legal = LegalTurns(s.board, s.turnColor, s.dice[0], s.dice[1])
		}
	case phaseOver:
		st.Phase = "over"
		if s.result != nil {
			kind := ""
			switch s.result.Points {
			case 2:
				kind = " (gammon, 2pts)"
			case 3:
				kind = " (backgammon, 3pts)"
			}
			st.Outcome = s.result.Winner.String() + " wins" + kind
		} else {
			st.Outcome = "aborted"
		}
	}
	return st
}
