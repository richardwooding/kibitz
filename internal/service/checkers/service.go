// The checkers service: game.Table lifecycle, on-demand Start, forced-move
// validation by membership, applyAndHash convention (hash after advance,
// identical on send and receive paths).
package checkers

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	ckengine "github.com/richardwooding/checkers"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// The checkers rules engine lives in its own module
// (github.com/richardwooding/checkers); re-export the API this service uses.
type (
	Board = ckengine.Board
	Side  = ckengine.Side
	Move  = ckengine.Move
)

const (
	Black = ckengine.Black
	White = ckengine.White
)

var (
	Start          = ckengine.Start
	LegalMoves     = ckengine.LegalMoves
	Validate       = ckengine.Validate
	Apply          = ckengine.Apply
	Winner         = ckengine.Winner
	ErrIllegalMove = ckengine.ErrIllegalMove
)

const ID = "checkers"

const (
	kindNewGame   uint8 = 1
	kindStartReq  uint8 = 2
	kindMove      uint8 = 3
	kindResign    uint8 = 4
	kindOfferDraw uint8 = 5
	kindAgreeDraw uint8 = 6
)

type msg struct {
	Kind      uint8  `cbor:"1,keyasint"`
	P1        uint32 `cbor:"2,keyasint,omitempty"`
	P2        uint32 `cbor:"3,keyasint,omitempty"`
	Path      []int8 `cbor:"4,keyasint,omitempty"`
	StateHash []byte `cbor:"5,keyasint,omitempty"`
}

type snapshot struct {
	Board  Board  `cbor:"1,keyasint"`
	P1     uint32 `cbor:"2,keyasint"`
	P2     uint32 `cbor:"3,keyasint"`
	Turn   uint8  `cbor:"4,keyasint"`
	Phase  uint8  `cbor:"5,keyasint"`
	Winner int8   `cbor:"6,keyasint"` // -1 none, 0/1 side, 2 draw
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing  bool
	Board    Board
	P1ID     wire.ParticipantID // black, moves first
	P2ID     wire.ParticipantID // white
	TurnID   wire.ParticipantID
	Legal    []Move // only populated for the mover
	Outcome  string // "", "black wins", "white wins", "draw"
	LastPath []int8
}

// Service implements service.Service.
type Service struct {
	ctx service.Context

	mu       sync.Mutex
	table    game.Table
	board    Board
	ph       game.Phase
	turn     Side
	winner   int8 // -1 in play, 0/1 winner side, 2 draw
	lastPath []int8
	drawFrom wire.ParticipantID
}

func New() *Service { return &Service{winner: -1} }

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
		s.winner = int8(winner)
		s.ph = game.Over
	}
	s.mu.Unlock()
	if forfeit {
		s.emitState()
	}
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
	s.turn = Black
	s.winner = -1
	s.lastPath = nil
	s.drawFrom = 0
}

// TryMove plays the local player's move (a full path).
func (s *Service) TryMove(path []int8) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(s.ctx.Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if err := Validate(s.board, side, Move(path)); err != nil {
		s.mu.Unlock()
		return err
	}
	s.board = Apply(s.board, side, Move(path))
	s.lastPath = path
	s.drawFrom = 0
	hash := s.applyAndHashLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindMove, Path: path, StateHash: hash})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// Resign concedes; OfferDraw/AgreeDraw follow the chess pattern.
func (s *Service) Resign() error {
	return s.finishAction(kindResign, func(side Side) {
		s.winner = int8(1 - side)
		s.ph = game.Over
	})
}

func (s *Service) OfferDraw() error {
	return s.finishAction(kindOfferDraw, nil)
}

func (s *Service) AgreeDraw() error {
	s.mu.Lock()
	pending := s.drawFrom != 0
	s.mu.Unlock()
	if !pending {
		return errors.New("checkers: no draw offer pending")
	}
	return s.finishAction(kindAgreeDraw, func(Side) {
		s.winner = 2
		s.ph = game.Over
		s.drawFrom = 0
	})
}

// finishAction validates seating, applies a local state change, and
// broadcasts the message kind.
func (s *Service) finishAction(kind uint8, apply func(Side)) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(s.ctx.Self)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("checkers: no game in progress")
	}
	if apply != nil {
		apply(Side(side))
	}
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kind})
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
		return fmt.Errorf("checkers: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		if from != s.ctx.HostID {
			return fmt.Errorf("checkers: new game from non-host %d", from)
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
	case kindMove:
		return s.handleMove(from, m)
	case kindResign:
		return s.handlePeerFinish(from, func(side Side) {
			s.winner = int8(1 - side)
			s.ph = game.Over
		})
	case kindOfferDraw:
		return s.handlePeerFinish(from, func(Side) {
			s.drawFrom = from
			s.ctx.Emit(DrawOffered{From: from})
		})
	case kindAgreeDraw:
		return s.handlePeerFinish(from, func(Side) {
			s.winner = 2
			s.ph = game.Over
			s.drawFrom = 0
		})
	}
	return fmt.Errorf("checkers: unknown message kind %d", m.Kind)
}

// DrawOffered is emitted when the opponent proposes a draw.
type DrawOffered struct{ From wire.ParticipantID }

func (s *Service) handlePeerFinish(from wire.ParticipantID, apply func(Side)) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("checkers: action outside game")
	}
	apply(Side(side))
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleMove(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if err := Validate(s.board, side, Move(m.Path)); err != nil {
		s.mu.Unlock()
		return err
	}
	s.board = Apply(s.board, side, Move(m.Path))
	s.lastPath = m.Path
	s.drawFrom = 0
	hash := s.applyAndHashLocked()
	ok := bytes.Equal(hash, m.StateHash)
	if !ok {
		s.ph = game.Over
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("checkers: position hash mismatch")
	}
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
		Turn: uint8(s.turn), Phase: uint8(s.ph), Winner: s.winner,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("checkers: restore: %w", err)
	}
	s.mu.Lock()
	// Late-joiner catch-up only (see chess/backgammon for why).
	if s.ph != game.Idle {
		s.mu.Unlock()
		return nil
	}
	s.board = snap.Board
	s.table.Seats = game.Seats{P1: wire.ParticipantID(snap.P1), P2: wire.ParticipantID(snap.P2)}
	s.turn = Side(snap.Turn)
	s.ph = game.Phase(snap.Phase)
	s.winner = snap.Winner
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

func (s *Service) checkTurnLocked(who wire.ParticipantID) (Side, error) {
	if s.ph != game.Playing {
		return 0, errors.New("checkers: no game in progress")
	}
	seatSide, seated := s.table.Seats.SideOf(who)
	if !seated {
		return 0, errors.New("checkers: not a player")
	}
	if Side(seatSide) != s.turn {
		return 0, errors.New("checkers: not your turn")
	}
	return s.turn, nil
}

// applyAndHashLocked advances the turn / resolves the outcome after a move,
// then hashes the post-advance state (the shared M3 convention).
func (s *Service) applyAndHashLocked() []byte {
	next := 1 - s.turn
	if winner, over := Winner(s.board, next); over {
		s.winner = int8(winner)
		s.ph = game.Over
	} else {
		s.turn = next
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
		return State{}
	}
	st := State{
		Playing:  true,
		Board:    s.board,
		P1ID:     s.table.Seats.P1,
		P2ID:     s.table.Seats.P2,
		LastPath: s.lastPath,
	}
	switch {
	case s.ph == game.Over && s.winner == 2:
		st.Outcome = "draw"
	case s.ph == game.Over && s.winner == 0:
		st.Outcome = "black wins"
	case s.ph == game.Over && s.winner == 1:
		st.Outcome = "white wins"
	case s.ph == game.Playing:
		st.TurnID = s.table.Seats.IDOf(game.Side(s.turn))
		if st.TurnID == s.ctx.Self {
			st.Legal = LegalMoves(s.board, s.turn)
		}
	}
	return st
}
