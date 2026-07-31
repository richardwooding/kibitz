// The Go / Weiqi service — 9×9, area scoring, komi 6.5 to white. Same shape as
// the Gomoku service (game.Table for seats/lifecycle, on-demand Start,
// both-sides-validate with a position hash computed identically on send and
// receive). A move is either a placement (row, col) or a pass; two consecutive
// passes end the game and the board is scored as it stands.
package weiqi

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

// ID is the service identifier on the wire. The UI shows the label "Go".
const ID = "weiqi"

const (
	kindNewGame        uint8 = 1
	kindStartReq       uint8 = 2
	kindPlace          uint8 = 3
	kindResign         uint8 = 4
	kindPass           uint8 = 5
	kindTakebackOffer  uint8 = 6
	kindTakebackAccept uint8 = 7
)

type msg struct {
	Kind      uint8  `cbor:"1,keyasint"`
	P1        uint32 `cbor:"2,keyasint,omitempty"`
	P2        uint32 `cbor:"3,keyasint,omitempty"`
	Row       int8   `cbor:"4,keyasint,omitempty"`
	Col       int8   `cbor:"5,keyasint,omitempty"`
	StateHash []byte `cbor:"6,keyasint,omitempty"`
}

type snapshot struct {
	Board   Board    `cbor:"1,keyasint"`
	Prev    Board    `cbor:"2,keyasint"` // ko: the forbidden (pre-last-move) position
	P1      uint32   `cbor:"3,keyasint"`
	P2      uint32   `cbor:"4,keyasint"`
	Turn    uint8    `cbor:"5,keyasint"`
	Phase   uint8    `cbor:"6,keyasint"`
	Winner  int8     `cbor:"7,keyasint"` // 0 none, 1 black, 2 white
	Last    int16    `cbor:"8,keyasint"`
	Passes  int      `cbor:"9,keyasint"` // consecutive passes
	CapB    int      `cbor:"10,keyasint"`
	CapW    int      `cbor:"11,keyasint"`
	History []string `cbor:"12,keyasint,omitempty"`
	// Stash is the pre-move state for 1-level takeback. Distinct from Prev
	// (which is the ko / forbidden position, part of the game rules).
	Stash []byte `cbor:"13,keyasint,omitempty"`
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing   bool
	Board     Board
	P1ID      wire.ParticipantID // black, moves first
	P2ID      wire.ParticipantID // white
	TurnID    wire.ParticipantID // 0 when over/idle
	Outcome   string             // "", "black wins", "white wins"
	Last      int16              // last placed stone's index, -1 when none/pass
	Legal     []int8             // legal placement points for the side to move
	History   []string           // ordered move notation, "⚫ e5" / "⚪ pass"
	CapturesB int                // stones captured by black
	CapturesW int                // stones captured by white
	Passed    bool               // the most recent move was a pass
	Passes    int                // consecutive passes so far
	ScoreB    float64            // black area (set when over)
	ScoreW    float64            // white area incl. komi (set when over)
	// CanTakeback: this end made the last move (or pass) and may offer to undo it.
	// TakebackBy: participant who has offered a takeback (0 = none).
	CanTakeback bool
	TakebackBy  wire.ParticipantID
}

// ErrNotTurn is returned when a player moves out of turn.
var ErrNotTurn = errors.New("weiqi: not your turn")

// Service implements service.Service; the mutex covers game state between the
// mux goroutine and UI calls.
type Service struct {
	service.Base

	mu      sync.Mutex
	table   game.Table
	board   Board
	prev    Board // board before the last applied move (ko / forbidden position)
	ph      game.Phase
	turn    game.Side
	winner  int8 // 0 in play, 1 black, 2 white
	last    int16
	passes  int
	capB    int
	capW    int
	history []string

	prevSnap []byte             // serialized pre-move state (1-level takeback)
	offerBy  wire.ParticipantID // who offered a takeback (0 = none)
}

// New constructs an idle Go service.
func New() *Service { return &Service{} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.SetContext(ctx) }

// OnPromote resets host-only seat bookkeeping when this end is promoted to host
// (migration); the next joiner re-seeds the opponent via NoteKeyed.
func (s *Service) OnPromote() {
	s.mu.Lock()
	s.table.OnPromote()
	s.mu.Unlock()
}

func (s *Service) MemberKeyed(id wire.ParticipantID, role session.Role) {
	if !s.Ctx().Host {
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
		s.forfeitLocked(winner)
	}
	s.mu.Unlock()
	if forfeit {
		s.emitState()
	}
}

// Start launches a game or rematch (host seats; players ask via startReq).
func (s *Service) Start() error {
	if !s.Ctx().Host {
		body, err := wire.Marshal(msg{Kind: kindStartReq})
		if err != nil {
			return err
		}
		return s.Ctx().Send.SendTo(s.Ctx().HostID, ID, body)
	}
	return s.hostStart(s.Ctx().Self)
}

func (s *Service) hostStart(from wire.ParticipantID) error {
	s.mu.Lock()
	if err := s.table.AuthorizeStart(s.Ctx().Host, from, s.Ctx().Self, s.ph); err != nil {
		s.mu.Unlock()
		return err
	}
	seats := s.table.NextSeats(s.Ctx().Self)
	s.resetLocked(seats)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindNewGame, P1: uint32(seats.P1), P2: uint32(seats.P2)})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) resetLocked(seats game.Seats) {
	s.board = Board{}
	s.prev = Board{}
	s.table.Seats = seats
	s.ph = game.Playing
	s.turn = game.P1
	s.winner = 0
	s.last = -1
	s.passes = 0
	s.capB = 0
	s.capW = 0
	s.history = nil
	s.prevSnap = nil
	s.offerBy = 0
	s.applyDepartedLocked()
}

// forfeitLocked ends the live game with winner by walkover.
func (s *Service) forfeitLocked(winner game.Side) {
	s.winner = int8(winner) + 1
	s.ph = game.Over
}

// applyDepartedLocked ends a just-installed live game whose seat pair still
// names a player that already left the session — the leave can overtake the
// newGame or snapshot that carried the seats (the relay orders frames per
// sender only; see Table.Departed).
func (s *Service) applyDepartedLocked() {
	if s.ph != game.Playing {
		return
	}
	if winner, forfeit := s.table.ApplyDeparted(); forfeit {
		s.forfeitLocked(winner)
	}
}

// disc returns the stone glyph for a seat (P1 black, P2 white).
func disc(side game.Side) string {
	if side == game.P2 {
		return "⚪"
	}
	return "⚫"
}

// notePlaceLocked appends a placement's notation ("⚫ e5", column letter +
// 1-based row, with " ×n" when it captured) — called on both the mover and
// receiver paths so every end builds the same list.
func (s *Service) notePlaceLocked(side game.Side, row, col int8, captured int) {
	note := fmt.Sprintf("%s %c%d", disc(side), 'a'+col, row+1)
	if captured > 0 {
		note += fmt.Sprintf(" ×%d", captured)
	}
	s.history = append(s.history, note)
}

// addCapturesLocked credits captured stones to the capturing side.
func (s *Service) addCapturesLocked(side game.Side, n int) {
	if side == game.P1 {
		s.capB += n
	} else {
		s.capW += n
	}
}

// placeLocked applies a placement to the board (both mover and receiver paths).
func (s *Service) placeLocked(side game.Side, row, col int8) error {
	if !inBounds(int(row), int(col)) {
		return ErrOffBoard
	}
	idx := int(row)*N + int(col)
	nb, captured, err := applyMove(s.board, int8(side)+1, idx, s.prev)
	if err != nil {
		return err
	}
	s.prev = s.board
	s.board = nb
	s.addCapturesLocked(side, len(captured))
	s.last = int16(idx)
	s.passes = 0
	s.notePlaceLocked(side, row, col, len(captured))
	s.turn = s.turn.Opponent()
	return nil
}

// passLocked applies a pass; two consecutive passes end the game and score it.
func (s *Service) passLocked(side game.Side) {
	s.prev = s.board // board unchanged; a pass releases any ko
	s.passes++
	s.last = -1
	s.history = append(s.history, fmt.Sprintf("%s pass", disc(side)))
	if s.passes >= 2 {
		_, _, s.winner = finalScore(s.board)
		s.ph = game.Over
	} else {
		s.turn = s.turn.Opponent()
	}
}

// Place plays the local player's stone at (row, col).
func (s *Service) Place(row, col int8) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(s.Ctx().Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	stash, _ := s.snapshotBlobLocked(nil) // pre-move state, committed only on a valid move
	if err := s.placeLocked(side, row, col); err != nil {
		s.mu.Unlock()
		return err
	}
	s.prevSnap = stash
	s.offerBy = 0
	hash := s.hashLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindPlace, Row: row, Col: col, StateHash: hash})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// Pass passes the local player's turn.
func (s *Service) Pass() error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(s.Ctx().Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	stash, _ := s.snapshotBlobLocked(nil) // pre-pass state, committed once the pass applies
	s.passLocked(side)
	s.prevSnap = stash
	s.offerBy = 0
	hash := s.hashLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindPass, StateHash: hash})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// Resign concedes.
func (s *Service) Resign() error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(s.Ctx().Self)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("weiqi: no game to resign")
	}
	s.winner = int8(side.Opponent()) + 1
	s.ph = game.Over
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindResign})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("weiqi: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		if from != s.Ctx().HostID {
			return fmt.Errorf("weiqi: new game from non-host %d", from)
		}
		s.mu.Lock()
		s.resetLocked(game.Seats{P1: wire.ParticipantID(m.P1), P2: wire.ParticipantID(m.P2)})
		s.mu.Unlock()
		s.emitState()
		return nil
	case kindStartReq:
		if !s.Ctx().Host {
			return nil
		}
		return s.hostStart(from)
	case kindPlace:
		return s.handlePlace(from, m)
	case kindPass:
		return s.handlePass(from, m)
	case kindResign:
		return s.handleResign(from)
	case kindTakebackOffer:
		return s.handleTakebackOffer(from)
	case kindTakebackAccept:
		return s.handleTakebackAccept(from)
	}
	return fmt.Errorf("weiqi: unknown message kind %d", m.Kind)
}

func (s *Service) handlePlace(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	stash, _ := s.snapshotBlobLocked(nil) // pre-move state, committed only on a valid move
	if err := s.placeLocked(side, m.Row, m.Col); err != nil {
		s.mu.Unlock()
		return err
	}
	s.prevSnap = stash
	s.offerBy = 0
	ok := s.verifyLocked(m.StateHash)
	s.mu.Unlock()
	if !ok {
		return errors.New("weiqi: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handlePass(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, err := s.checkTurnLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	stash, _ := s.snapshotBlobLocked(nil) // pre-pass state, committed once the pass applies
	s.passLocked(side)
	s.prevSnap = stash
	s.offerBy = 0
	ok := s.verifyLocked(m.StateHash)
	s.mu.Unlock()
	if !ok {
		return errors.New("weiqi: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("weiqi: resign outside game")
	}
	s.winner = int8(side.Opponent()) + 1
	s.ph = game.Over
	s.mu.Unlock()
	s.emitState()
	return nil
}

// OfferTakeback asks the opponent to undo this end's last move or pass. Valid
// only for the last mover (it's the opponent's turn now) while a stash exists.
func (s *Service) OfferTakeback() error {
	s.mu.Lock()
	if !s.canOfferLocked(s.Ctx().Self) {
		s.mu.Unlock()
		return errors.New("weiqi: no takeback available")
	}
	s.offerBy = s.Ctx().Self
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindTakebackOffer})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
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
	if s.offerBy == 0 || s.offerBy == s.Ctx().Self || s.ph != game.Playing {
		s.mu.Unlock()
		return errors.New("weiqi: no takeback to accept")
	}
	s.revertLocked()
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindTakebackAccept})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
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

// revertLocked restores the pre-move state stashed in prevSnap (1-level undo) —
// board, ko/prev position, seats, turn, winner, last, consecutive passes,
// capture counts and history. Every end reverts from its own identical stash,
// so no state rides the wire.
func (s *Service) revertLocked() {
	if s.prevSnap == nil {
		return
	}
	snap, err := wire.Body[snapshot](s.prevSnap)
	if err != nil {
		return
	}
	s.board = snap.Board
	s.prev = snap.Prev
	s.table.Seats = game.Seats{P1: wire.ParticipantID(snap.P1), P2: wire.ParticipantID(snap.P2)}
	s.turn = game.Side(snap.Turn)
	s.ph = game.Phase(snap.Phase)
	s.winner = snap.Winner
	s.last = snap.Last
	s.passes = snap.Passes
	s.capB = snap.CapB
	s.capW = snap.CapW
	s.history = snap.History
	s.prevSnap = snap.Stash
	s.offerBy = 0
}

// verifyLocked compares the post-move hash with the mover's; on mismatch it
// ends the game (desync) and reports false.
func (s *Service) verifyLocked(want []byte) bool {
	if bytes.Equal(s.hashLocked(), want) {
		return true
	}
	s.ph = game.Over
	return false
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ph == game.Idle {
		return nil, nil
	}
	return s.snapshotBlobLocked(s.prevSnap)
}

// snapshotBlobLocked serializes the current state. stash is embedded so a late
// joiner inherits the pre-move takeback stash; it is nil when stashing prevSnap
// itself, to avoid unbounded nesting. Note Prev is the ko position (game rules),
// distinct from Stash (the takeback undo state).
func (s *Service) snapshotBlobLocked(stash []byte) ([]byte, error) {
	return wire.Marshal(snapshot{
		Board: s.board, Prev: s.prev,
		P1: uint32(s.table.Seats.P1), P2: uint32(s.table.Seats.P2),
		Turn: uint8(s.turn), Phase: uint8(s.ph), Winner: s.winner, Last: s.last,
		Passes: s.passes, CapB: s.capB, CapW: s.capW, History: s.history,
		Stash: stash,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("weiqi: restore: %w", err)
	}
	s.mu.Lock()
	// Late-joiner catch-up only (see chess/backgammon for why).
	if s.ph != game.Idle {
		s.mu.Unlock()
		return nil
	}
	s.board = snap.Board
	s.prev = snap.Prev
	s.table.Seats = game.Seats{P1: wire.ParticipantID(snap.P1), P2: wire.ParticipantID(snap.P2)}
	s.turn = game.Side(snap.Turn)
	s.ph = game.Phase(snap.Phase)
	s.winner = snap.Winner
	s.last = snap.Last
	s.passes = snap.Passes
	s.capB = snap.CapB
	s.capW = snap.CapW
	s.history = snap.History
	s.prevSnap = snap.Stash
	s.applyDepartedLocked()
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
		return 0, errors.New("weiqi: no game in progress")
	}
	side, seated := s.table.Seats.SideOf(who)
	if !seated {
		return 0, errors.New("weiqi: not a player")
	}
	if side != s.turn {
		return 0, ErrNotTurn
	}
	return side, nil
}

// hashLocked hashes the canonical post-move state — board, ko (prev), turn,
// phase and the consecutive-pass count — identically on the send and receive
// paths (the both-sides-validate convention).
func (s *Service) hashLocked() []byte {
	b, err := wire.Marshal(struct {
		Board  Board `cbor:"1,keyasint"`
		Prev   Board `cbor:"2,keyasint"`
		Turn   uint8 `cbor:"3,keyasint"`
		Phase  uint8 `cbor:"4,keyasint"`
		Passes int   `cbor:"5,keyasint"`
	}{s.board, s.prev, uint8(s.turn), uint8(s.ph), s.passes})
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
	s.Ctx().Emit(st)
}

func (s *Service) stateLocked() State {
	if s.ph == game.Idle {
		return State{Last: -1}
	}
	st := State{
		Playing:   true,
		Board:     s.board,
		P1ID:      s.table.Seats.P1,
		P2ID:      s.table.Seats.P2,
		Last:      s.last,
		Passed:    s.passes > 0,
		Passes:    s.passes,
		CapturesB: s.capB,
		CapturesW: s.capW,
		History:   append([]string(nil), s.history...),
	}
	if s.ph == game.Over {
		s.fillOutcomeLocked(&st)
	} else {
		st.TurnID = s.table.Seats.IDOf(s.turn)
		st.Legal = legalMoves(s.board, int8(s.turn)+1, s.prev)
	}
	side, seated := s.table.Seats.SideOf(s.Ctx().Self)
	st.CanTakeback = seated && s.ph == game.Playing && s.prevSnap != nil && s.turn != side && s.offerBy == 0
	st.TakebackBy = s.offerBy
	return st
}

// fillOutcomeLocked sets the winner string and final area scores.
func (s *Service) fillOutcomeLocked(st *State) {
	switch s.winner {
	case 1:
		st.Outcome = "black wins"
	case 2:
		st.Outcome = "white wins"
	}
	st.ScoreB, st.ScoreW, _ = finalScore(s.board)
}
