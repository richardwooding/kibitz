// The Dots and Boxes service — same shape as the Gomoku service (game.Table
// for seats/lifecycle, on-demand Start, both-sides-validate with a position
// hash computed identically on send and receive). The one twist: completing a
// box grants another turn, so the turn only passes when a move completes zero
// boxes.
package dots

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

const ID = "dots"

const (
	kindNewGame        uint8 = 1
	kindStartReq       uint8 = 2
	kindDraw           uint8 = 3
	kindResign         uint8 = 4
	kindTakebackOffer  uint8 = 5
	kindTakebackAccept uint8 = 6
)

type msg struct {
	Kind      uint8  `cbor:"1,keyasint"`
	P1        uint32 `cbor:"2,keyasint,omitempty"`
	P2        uint32 `cbor:"3,keyasint,omitempty"`
	Edge      int8   `cbor:"4,keyasint,omitempty"`
	StateHash []byte `cbor:"5,keyasint,omitempty"`
}

type snapshot struct {
	Board   Board    `cbor:"1,keyasint"`
	P1      uint32   `cbor:"2,keyasint"`
	P2      uint32   `cbor:"3,keyasint"`
	Turn    uint8    `cbor:"4,keyasint"`
	Phase   uint8    `cbor:"5,keyasint"`
	Winner  int8     `cbor:"6,keyasint"` // 0 none, 1/2 side, 3 draw
	Last    int8     `cbor:"7,keyasint"`
	History []string `cbor:"8,keyasint,omitempty"`
	Prev    []byte   `cbor:"9,keyasint,omitempty"` // pre-move state for 1-level takeback
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing bool
	Edges   [NumEdges]int8 // 0 undrawn, 1 drawn
	Boxes   [NumBoxes]int8 // 0 none, 1 P1 (red), 2 P2 (blue)
	ScoreP1 int
	ScoreP2 int
	P1ID    wire.ParticipantID // red, moves first
	P2ID    wire.ParticipantID // blue
	TurnID  wire.ParticipantID // 0 when over/idle
	Outcome string             // "", "red wins 13–12", "blue wins …", "draw …"
	Last    int8               // last drawn edge, -1 when none
	History []string           // ordered move notation, "🟥 —c2" / "🟦 |d3"
	Legal   []int8             // undrawn edge ids (bot & UI)
	// CanTakeback: this end made the last move and may offer to undo it.
	// TakebackBy: participant who has offered a takeback (0 = none).
	CanTakeback bool
	TakebackBy  wire.ParticipantID
}

var ErrNotTurn = errors.New("dots: not your turn")

// Service implements service.Service; the mutex covers game state between the
// mux goroutine and UI calls.
type Service struct {
	ctx service.Context

	mu      sync.Mutex
	table   game.Table
	board   Board
	ph      game.Phase
	turn    game.Side
	winner  int8 // 0 in play, 1/2 winner, 3 draw
	last    int8
	history []string

	prevSnap []byte             // serialized pre-move state (1-level takeback)
	offerBy  wire.ParticipantID // who offered a takeback (0 = none)
}

func New() *Service { return &Service{} }

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
	s.board = Board{}
	s.table.Seats = seats
	s.ph = game.Playing
	s.turn = game.P1
	s.winner = 0
	s.last = -1
	s.history = nil
	s.prevSnap = nil
	s.offerBy = 0
}

// noteDrawLocked appends a move's notation ("🟥 —c2" / "🟦 |d3": side glyph +
// edge label) — called on both the mover and receiver paths so every end
// builds the same list.
func (s *Service) noteDrawLocked(side game.Side, edge int8) {
	glyph := "🟥"
	if side == game.P2 {
		glyph = "🟦"
	}
	s.history = append(s.history, fmt.Sprintf("%s %s", glyph, EdgeLabel(edge)))
}

// DrawEdge draws the local player's edge.
func (s *Service) DrawEdge(edge int8) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(s.ctx.Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	prev, _ := s.snapshotBlobLocked(nil) // pre-move state, committed only on a valid move
	claimed, err := s.board.Apply(edge, int8(side)+1)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.prevSnap = prev
	s.offerBy = 0
	s.last = edge
	s.noteDrawLocked(side, edge)
	hash := s.applyAndHashLocked(claimed)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindDraw, Edge: edge, StateHash: hash})
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
		return errors.New("dots: no game to resign")
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
		return fmt.Errorf("dots: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		if from != s.ctx.HostID {
			return fmt.Errorf("dots: new game from non-host %d", from)
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
	case kindDraw:
		return s.handleDraw(from, m)
	case kindResign:
		return s.handleResign(from)
	case kindTakebackOffer:
		return s.handleTakebackOffer(from)
	case kindTakebackAccept:
		return s.handleTakebackAccept(from)
	}
	return fmt.Errorf("dots: unknown message kind %d", m.Kind)
}

// OfferTakeback asks the opponent to undo this end's last move. Valid only for
// the last mover (it's the opponent's turn now) while a stashed move exists.
func (s *Service) OfferTakeback() error {
	s.mu.Lock()
	if !s.canOfferLocked(s.ctx.Self) {
		s.mu.Unlock()
		return errors.New("dots: no takeback available")
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
		return errors.New("dots: no takeback to accept")
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
// Restoring the board recovers edges, boxes and scores together; turn and
// winner come back too, so the Dots extra-turn semantics fall out naturally.
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
	s.last = snap.Last
	s.history = snap.History
	s.prevSnap = snap.Prev
	s.offerBy = 0
}

func (s *Service) handleDraw(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	prev, _ := s.snapshotBlobLocked(nil) // pre-move state, committed only on a valid move
	claimed, err := s.board.Apply(m.Edge, int8(side)+1)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.prevSnap = prev
	s.offerBy = 0
	s.last = m.Edge
	s.noteDrawLocked(side, m.Edge)
	hash := s.applyAndHashLocked(claimed)
	ok := bytes.Equal(hash, m.StateHash)
	if !ok {
		s.ph = game.Over
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("dots: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("dots: resign outside game")
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
		Turn: uint8(s.turn), Phase: uint8(s.ph), Winner: s.winner, Last: s.last,
		History: s.history, Prev: prev,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("dots: restore: %w", err)
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
	s.last = snap.Last
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

func (s *Service) checkTurnLocked(who wire.ParticipantID) (game.Side, error) {
	if s.ph != game.Playing {
		return 0, errors.New("dots: no game in progress")
	}
	side, seated := s.table.Seats.SideOf(who)
	if !seated {
		return 0, errors.New("dots: not a player")
	}
	if side != s.turn {
		return 0, ErrNotTurn
	}
	return side, nil
}

// applyAndHashLocked resolves the outcome after a draw, then hashes the
// post-move state. Called identically on both the send and receive paths. The
// Dots twist: the turn passes only when the move completed ZERO boxes; a move
// that claimed one or two boxes keeps the same side on turn.
func (s *Service) applyAndHashLocked(claimed int) []byte {
	if s.board.Full() {
		s.finishLocked()
	} else if claimed == 0 {
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

// finishLocked ends the game and sets the winner by box count (3 = draw).
func (s *Service) finishLocked() {
	p1, p2 := s.board.Score()
	switch {
	case p1 > p2:
		s.winner = 1
	case p2 > p1:
		s.winner = 2
	default:
		s.winner = 3
	}
	s.ph = game.Over
}

func (s *Service) emitState() {
	s.mu.Lock()
	st := s.stateLocked()
	s.mu.Unlock()
	s.ctx.Emit(st)
}

// outcomeText phrases the result by box count, e.g. "red wins 13–12".
func (s *Service) outcomeText() string {
	p1, p2 := s.board.Score()
	switch s.winner {
	case 1:
		return fmt.Sprintf("red wins %d–%d", p1, p2)
	case 2:
		return fmt.Sprintf("blue wins %d–%d", p2, p1)
	default:
		return fmt.Sprintf("draw %d–%d", p1, p2)
	}
}

func (s *Service) stateLocked() State {
	if s.ph == game.Idle {
		return State{Last: -1}
	}
	p1, p2 := s.board.Score()
	st := State{
		Playing: true,
		Edges:   s.board.Edges,
		Boxes:   s.board.Owner,
		ScoreP1: p1,
		ScoreP2: p2,
		P1ID:    s.table.Seats.P1,
		P2ID:    s.table.Seats.P2,
		Last:    s.last,
		History: append([]string(nil), s.history...),
		Legal:   s.board.Legal(),
	}
	if s.ph == game.Over {
		st.Outcome = s.outcomeText()
	} else {
		st.TurnID = s.table.Seats.IDOf(s.turn)
	}
	side, seated := s.table.Seats.SideOf(s.ctx.Self)
	st.CanTakeback = seated && s.ph == game.Playing && s.prevSnap != nil && s.turn != side && s.offerBy == 0
	st.TakebackBy = s.offerBy
	return st
}
