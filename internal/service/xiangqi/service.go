// The Xiangqi (Chinese chess) service — same shape as the Gomoku/Checkers
// services (game.Table for seats/lifecycle, on-demand Start, both-sides-
// validate with a position hash computed identically on send and receive). A
// move is a (from,to) pair of 0..89 board indices; the deterministic engine
// in engine.go validates it and every end verifies the resulting position hash.
package xiangqi

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

// ID is the service identifier on the wire and in the picker.
const ID = "xiangqi"

const (
	kindNewGame        uint8 = 1
	kindStartReq       uint8 = 2
	kindMove           uint8 = 3
	kindResign         uint8 = 4
	kindTakebackOffer  uint8 = 5
	kindTakebackAccept uint8 = 6
)

type msg struct {
	Kind      uint8  `cbor:"1,keyasint"`
	P1        uint32 `cbor:"2,keyasint,omitempty"`
	P2        uint32 `cbor:"3,keyasint,omitempty"`
	From      int8   `cbor:"4,keyasint,omitempty"`
	To        int8   `cbor:"5,keyasint,omitempty"`
	StateHash []byte `cbor:"6,keyasint,omitempty"`
}

type snapshot struct {
	Board    Board    `cbor:"1,keyasint"`
	P1       uint32   `cbor:"2,keyasint"`
	P2       uint32   `cbor:"3,keyasint"`
	Turn     uint8    `cbor:"4,keyasint"`
	Phase    uint8    `cbor:"5,keyasint"`
	Winner   int8     `cbor:"6,keyasint"` // 0 none, 1 red, 2 black
	LastFrom int8     `cbor:"7,keyasint"`
	LastTo   int8     `cbor:"8,keyasint"`
	History  []string `cbor:"9,keyasint,omitempty"`
	Prev     []byte   `cbor:"10,keyasint,omitempty"` // pre-move state for 1-level takeback
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing  bool
	Board    Board
	P1ID     wire.ParticipantID // red, moves first
	P2ID     wire.ParticipantID // black
	TurnID   wire.ParticipantID // 0 when over/idle
	Outcome  string             // "", "red wins", "black wins"
	History  []string           // ordered move notation, "🔴 車 h3-e3"
	LastFrom int8               // last move's origin, -1 when none
	LastTo   int8               // last move's destination, -1 when none
	InCheck  bool               // side to move is in check (UI hint)
	Legal    [][2]int8          // all legal {from,to} for the side to move
	// CanTakeback: this end made the last move and may offer to undo it.
	// TakebackBy: participant who has offered a takeback (0 = none).
	CanTakeback bool
	TakebackBy  wire.ParticipantID
}

// ErrNotTurn is returned when a player tries to move out of turn.
var ErrNotTurn = errors.New("xiangqi: not your turn")

// Service implements service.Service; the mutex covers game state between the
// mux goroutine and UI calls.
type Service struct {
	ctx service.Context

	mu       sync.Mutex
	table    game.Table
	board    Board
	ph       game.Phase
	turn     game.Side
	winner   int8 // 0 in play, 1 red wins, 2 black wins
	lastFrom int8
	lastTo   int8
	history  []string

	prevSnap []byte             // serialized pre-move state (1-level takeback)
	offerBy  wire.ParticipantID // who offered a takeback (0 = none)
}

// New constructs an idle Xiangqi service.
func New() *Service { return &Service{lastFrom: -1, lastTo: -1} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.ctx = ctx }

// OnPromote resets host-only seat bookkeeping when this end is promoted to host
// (migration); the next joiner re-seeds the opponent via NoteKeyed.
func (s *Service) OnPromote() {
	s.mu.Lock()
	s.table.OnPromote()
	s.mu.Unlock()
}

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
	s.board = Start()
	s.table.Seats = seats
	s.ph = game.Playing
	s.turn = game.P1
	s.winner = 0
	s.lastFrom = -1
	s.lastTo = -1
	s.history = nil
	s.prevSnap = nil
	s.offerBy = 0
}

// noteLocked appends a move's notation ("🔴 車 h3-e3"): side glyph + piece glyph
// + from→to coords. Called on both mover and receiver paths, BEFORE the board
// is updated (it reads the moving piece from its origin).
func (s *Service) noteLocked(from, to int8) {
	p := s.board[from]
	side := "🔴"
	if p < 0 {
		side = "⚫"
	}
	s.history = append(s.history, fmt.Sprintf("%s %s %s-%s", side, Glyph(p), Coord(from), Coord(to)))
}

// Move plays the local player's move from→to (0..89 board indices).
func (s *Service) Move(from, to int8) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(s.ctx.Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if err := Validate(s.board, sideSign(side), from, to); err != nil {
		s.mu.Unlock()
		return err
	}
	prev, _ := s.snapshotBlobLocked(nil) // pre-move state, committed only on a valid move
	s.noteLocked(from, to)
	s.board = Apply(s.board, from, to)
	s.prevSnap = prev
	s.offerBy = 0
	s.lastFrom, s.lastTo = from, to
	hash := s.applyAndHashLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindMove, From: from, To: to, StateHash: hash})
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
		return errors.New("xiangqi: no game to resign")
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
		return fmt.Errorf("xiangqi: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		if from != s.ctx.HostID {
			return fmt.Errorf("xiangqi: new game from non-host %d", from)
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
		return s.handleResign(from)
	case kindTakebackOffer:
		return s.handleTakebackOffer(from)
	case kindTakebackAccept:
		return s.handleTakebackAccept(from)
	}
	return fmt.Errorf("xiangqi: unknown message kind %d", m.Kind)
}

// OfferTakeback asks the opponent to undo this end's last move. Valid only for
// the last mover (it's the opponent's turn now) while a stashed move exists.
func (s *Service) OfferTakeback() error {
	s.mu.Lock()
	if !s.canOfferLocked(s.ctx.Self) {
		s.mu.Unlock()
		return errors.New("xiangqi: no takeback available")
	}
	s.offerBy = s.ctx.Self
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindTakebackOffer})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) canOfferLocked(who wire.ParticipantID) bool {
	side, seated := s.table.Seats.SideOf(who)
	return seated && s.ph == game.Playing && s.prevSnap != nil && s.turn != side
}

func (s *Service) handleTakebackOffer(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.canOfferLocked(from) {
		s.offerBy = from
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

// AcceptTakeback accepts a pending offer from the opponent and reverts one move.
func (s *Service) AcceptTakeback() error {
	s.mu.Lock()
	if s.offerBy == 0 || s.offerBy == s.ctx.Self || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("xiangqi: no takeback to accept")
	}
	s.revertLocked()
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindTakebackAccept})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleTakebackAccept(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.offerBy != 0 && from != s.offerBy {
		s.revertLocked()
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

// revertLocked restores the pre-move state stashed in prevSnap (1-level undo).
// Every end reverts from its own identical stash, so no state rides the wire.
func (s *Service) revertLocked() {
	if s.prevSnap == nil {
		return
	}
	snap, err := wire.Body[snapshot](s.prevSnap)
	if err != nil {
		return
	}
	s.board = snap.Board
	s.table.Seats = game.Seats{P1: wire.ParticipantID(snap.P1), P2: wire.ParticipantID(snap.P2)}
	s.turn = game.Side(snap.Turn)
	s.ph = game.Phase(snap.Phase)
	s.winner = snap.Winner
	s.lastFrom = snap.LastFrom
	s.lastTo = snap.LastTo
	s.history = snap.History
	s.prevSnap = snap.Prev
	s.offerBy = 0
}

func (s *Service) handleMove(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if err := Validate(s.board, sideSign(side), m.From, m.To); err != nil {
		s.mu.Unlock()
		return err
	}
	prev, _ := s.snapshotBlobLocked(nil) // pre-move state, committed only on a valid move
	s.noteLocked(m.From, m.To)
	s.board = Apply(s.board, m.From, m.To)
	s.prevSnap = prev
	s.offerBy = 0
	s.lastFrom, s.lastTo = m.From, m.To
	hash := s.applyAndHashLocked()
	ok := bytes.Equal(hash, m.StateHash)
	if !ok {
		s.ph = game.Over
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("xiangqi: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("xiangqi: resign outside game")
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
	return s.snapshotBlobLocked(s.prevSnap)
}

// snapshotBlobLocked serializes the current state. prev is embedded so a late
// joiner inherits the pre-move stash and can take part in a takeback; it is nil
// when stashing prevSnap itself, to avoid unbounded nesting.
func (s *Service) snapshotBlobLocked(prev []byte) ([]byte, error) {
	return wire.Marshal(snapshot{
		Board: s.board, P1: uint32(s.table.Seats.P1), P2: uint32(s.table.Seats.P2),
		Turn: uint8(s.turn), Phase: uint8(s.ph), Winner: s.winner,
		LastFrom: s.lastFrom, LastTo: s.lastTo, History: s.history, Prev: prev,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("xiangqi: restore: %w", err)
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
	s.lastFrom = snap.LastFrom
	s.lastTo = snap.LastTo
	s.history = snap.History
	s.prevSnap = snap.Prev
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

// sideSign maps a seat (P1 red / P2 black) to the engine's signed side.
func sideSign(side game.Side) int8 {
	if side == game.P1 {
		return Red
	}
	return Black
}

func (s *Service) checkTurnLocked(who wire.ParticipantID) (game.Side, error) {
	if s.ph != game.Playing {
		return 0, errors.New("xiangqi: no game in progress")
	}
	side, seated := s.table.Seats.SideOf(who)
	if !seated {
		return 0, errors.New("xiangqi: not a player")
	}
	if side != s.turn {
		return 0, ErrNotTurn
	}
	return side, nil
}

// applyAndHashLocked resolves the outcome after a move (the side to move next
// with no legal reply loses), then hashes the post-move state. Called
// identically on the send and receive paths — the shared hash convention.
func (s *Service) applyAndHashLocked() []byte {
	next := s.turn.Opponent()
	if _, over := Winner(s.board, sideSign(next)); over {
		s.winner = int8(s.turn) + 1 // the side that just moved wins
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
		return State{LastFrom: -1, LastTo: -1}
	}
	st := State{
		Playing:  true,
		Board:    s.board,
		P1ID:     s.table.Seats.P1,
		P2ID:     s.table.Seats.P2,
		LastFrom: s.lastFrom,
		LastTo:   s.lastTo,
		History:  append([]string(nil), s.history...),
	}
	switch {
	case s.ph == game.Over && s.winner == 1:
		st.Outcome = "red wins"
	case s.ph == game.Over && s.winner == 2:
		st.Outcome = "black wins"
	case s.ph == game.Playing:
		st.TurnID = s.table.Seats.IDOf(s.turn)
		st.InCheck = InCheck(s.board, sideSign(s.turn))
		if st.TurnID == s.ctx.Self {
			st.Legal = LegalMoves(s.board, sideSign(s.turn))
		}
	}
	side, seated := s.table.Seats.SideOf(s.ctx.Self)
	st.CanTakeback = seated && s.ph == game.Playing && s.prevSnap != nil && s.turn != side && s.offerBy == 0
	st.TakebackBy = s.offerBy
	return st
}
