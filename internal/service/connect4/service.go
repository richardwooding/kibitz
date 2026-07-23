// The Connect Four service — the template for M3's open-information games:
// game.Table for seats/lifecycle, on-demand Start, both-sides-validate with
// a position hash computed by applyAndHash identically on send and receive.
package connect4

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

const ID = "connect4"

const (
	kindNewGame  uint8 = 1
	kindStartReq uint8 = 2
	kindDrop     uint8 = 3
	kindResign   uint8 = 4
)

type msg struct {
	Kind      uint8  `cbor:"1,keyasint"`
	P1        uint32 `cbor:"2,keyasint,omitempty"`
	P2        uint32 `cbor:"3,keyasint,omitempty"`
	Col       int8   `cbor:"4,keyasint,omitempty"`
	StateHash []byte `cbor:"5,keyasint,omitempty"`
}

type snapshot struct {
	Board   Board  `cbor:"1,keyasint"`
	P1      uint32 `cbor:"2,keyasint"`
	P2      uint32 `cbor:"3,keyasint"`
	Turn    uint8  `cbor:"4,keyasint"`
	Phase   uint8  `cbor:"5,keyasint"`
	Winner  int8   `cbor:"6,keyasint"` // 0 none, 1/2 side, 3 draw
	LastCol int8   `cbor:"7,keyasint"`
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing  bool
	Board    Board
	P1ID     wire.ParticipantID // red, moves first
	P2ID     wire.ParticipantID // yellow
	TurnID   wire.ParticipantID // 0 when over/idle
	Outcome  string             // "", "red wins", "yellow wins", "draw"
	WinCells []int8
	LastCol  int8 // -1 when none
}

var ErrNotTurn = errors.New("connect4: not your turn")

// Service implements service.Service; the mutex covers game state between
// the mux goroutine and UI calls.
type Service struct {
	ctx service.Context

	mu      sync.Mutex
	table   game.Table
	board   Board
	ph      game.Phase
	turn    game.Side
	winner  int8 // 0 in play, 1/2 winner, 3 draw
	lastCol int8
}

func New() *Service { return &Service{} }

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
		s.winner = int8(winner) + 1
		s.ph = game.Over
	}
	s.mu.Unlock()
	if forfeit {
		s.emitState()
	}
}

// Start launches a game or rematch (host seats; players ask via startReq).
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
	s.board = Board{}
	s.table.Seats = seats
	s.ph = game.Playing
	s.turn = game.P1
	s.winner = 0
	s.lastCol = -1
}

// Drop plays the local player's disc in col.
func (s *Service) Drop(col int8) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(s.ctx.Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if _, err := s.board.Drop(col, int8(side)+1); err != nil {
		s.mu.Unlock()
		return err
	}
	s.lastCol = col
	hash := s.applyAndHashLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindDrop, Col: col, StateHash: hash})
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
		return errors.New("connect4: no game to resign")
	}
	s.winner = int8(side.Opponent()) + 1
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
		return fmt.Errorf("connect4: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		if from != s.ctx.HostID {
			return fmt.Errorf("connect4: new game from non-host %d", from)
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
	case kindDrop:
		return s.handleDrop(from, m)
	case kindResign:
		return s.handleResign(from)
	}
	return fmt.Errorf("connect4: unknown message kind %d", m.Kind)
}

func (s *Service) handleDrop(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if _, err := s.board.Drop(m.Col, int8(side)+1); err != nil {
		s.mu.Unlock()
		return err
	}
	s.lastCol = m.Col
	hash := s.applyAndHashLocked()
	ok := bytes.Equal(hash, m.StateHash)
	if !ok {
		s.ph = game.Over
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("connect4: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("connect4: resign outside game")
	}
	s.winner = int8(side.Opponent()) + 1
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
		Turn: uint8(s.turn), Phase: uint8(s.ph), Winner: s.winner, LastCol: s.lastCol,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("connect4: restore: %w", err)
	}
	s.mu.Lock()
	// Late-joiner catch-up only (see chess/backgammon for why).
	if s.ph != game.Idle {
		s.mu.Unlock()
		return nil
	}
	s.board = snap.Board
	s.table.Seats = game.Seats{P1: wire.ParticipantID(snap.P1), P2: wire.ParticipantID(snap.P2)}
	s.turn = game.Side(snap.Turn)
	s.ph = game.Phase(snap.Phase)
	s.winner = snap.Winner
	s.lastCol = snap.LastCol
	s.mu.Unlock()
	s.emitState()
	return nil
}

// State returns the current game state for UI pulls.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

// --- internals ---------------------------------------------------------------

func (s *Service) checkTurnLocked(who wire.ParticipantID) (game.Side, error) {
	if s.ph != game.Playing {
		return 0, errors.New("connect4: no game in progress")
	}
	side, seated := s.table.Seats.SideOf(who)
	if !seated {
		return 0, errors.New("connect4: not a player")
	}
	if side != s.turn {
		return 0, ErrNotTurn
	}
	return side, nil
}

// applyAndHashLocked advances the turn / resolves the outcome after a drop,
// then hashes the post-advance state. Called identically on both the send
// and receive paths — the hash convention every M3 game shares.
func (s *Service) applyAndHashLocked() []byte {
	if w, _ := s.board.Winner(); w != 0 {
		s.winner = w
		s.ph = game.Over
	} else if s.board.Full() {
		s.winner = 3
		s.ph = game.Over
	} else {
		s.turn = s.turn.Opponent()
	}
	b, err := wire.Marshal(struct {
		Board Board `cbor:"1,keyasint"`
		Turn  uint8 `cbor:"2,keyasint"`
		Phase uint8 `cbor:"3,keyasint"`
	}{s.board, uint8(s.turn), uint8(s.ph)})
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
		return State{LastCol: -1}
	}
	st := State{
		Playing: true,
		Board:   s.board,
		P1ID:    s.table.Seats.P1,
		P2ID:    s.table.Seats.P2,
		LastCol: s.lastCol,
	}
	switch {
	case s.ph == game.Over && s.winner == 3:
		st.Outcome = "draw"
	case s.ph == game.Over && s.winner == 1:
		st.Outcome = "red wins"
	case s.ph == game.Over && s.winner == 2:
		st.Outcome = "yellow wins"
	default:
		st.TurnID = s.table.Seats.IDOf(s.turn)
	}
	if _, cells := s.board.Winner(); cells != nil {
		st.WinCells = cells
	}
	return st
}
