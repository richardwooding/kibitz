// The Gomoku Party service — five-in-a-row for 2–4 players on a shared 19×19
// board with rotating turns. Same both-sides-validate shape as the two-player
// gomoku/connect4 services (a move is a free (row,col) placement; a position
// hash is computed identically on send and receive), but backed by game.Ring
// (N ordered seats + a rotating turn) instead of the two-seat game.Table.
//
// Seating is host-authoritative: the host accumulates keyed members, builds the
// seat list at Start, and broadcasts it; every end adopts it verbatim. Play is
// peer-to-peer both-sides-validate; a mover's color is derived from its seat,
// never trusted from the wire.
package gomokup

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

const ID = "gomokup"

// MaxSeats is the player cap for a single game.
const MaxSeats = 4

const (
	kindNewGame  uint8 = 1
	kindStartReq uint8 = 2
	kindPlace    uint8 = 3
	kindResign   uint8 = 4
)

type msg struct {
	Kind      uint8    `cbor:"1,keyasint"`
	Seats     []uint32 `cbor:"2,keyasint,omitempty"` // new-game seat list, seat order
	Turn      uint8    `cbor:"3,keyasint,omitempty"` // opening seat index
	Row       int8     `cbor:"4,keyasint,omitempty"`
	Col       int8     `cbor:"5,keyasint,omitempty"`
	StateHash []byte   `cbor:"6,keyasint,omitempty"`
}

type snapshot struct {
	Board   Board    `cbor:"1,keyasint"`
	Seats   []uint32 `cbor:"2,keyasint,omitempty"`
	Turn    uint8    `cbor:"3,keyasint"`
	Phase   uint8    `cbor:"4,keyasint"`
	Winner  int8     `cbor:"5,keyasint"` // 0 none/abandoned, else winning seat+1
	Draw    bool     `cbor:"6,keyasint,omitempty"`
	Last    int16    `cbor:"7,keyasint"`
	History []string `cbor:"8,keyasint,omitempty"`
	Gone    []bool   `cbor:"9,keyasint,omitempty"`
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing  bool
	Board    Board
	Seats    []wire.ParticipantID // ordered seats; index = color-1
	Gone     []bool               // per-seat: left/resigned
	TurnID   wire.ParticipantID   // 0 when over/idle
	WinnerID wire.ParticipantID   // 0 = none/abandoned/draw
	Draw     bool
	Outcome  string // "", "seat N wins", "abandoned", "draw" (UI shows names)
	WinCells []int16
	Last     int16    // last placed stone's index, -1 when none
	History  []string // ordered notation, "🔴 h8"
}

// stoneGlyph is the notation marker per seat color (index 0..MaxSeats-1).
var stoneGlyph = [MaxSeats]string{"🔴", "🔵", "🟢", "🟡"}

var (
	ErrNotTurn  = errors.New("gomokup: not your turn")
	ErrNoGame   = errors.New("gomokup: no game in progress")
	ErrNotSeat  = errors.New("gomokup: not a player in this game")
	ErrNoResign = errors.New("gomokup: no game to resign")
)

// Service implements service.Service; the mutex covers game state between the
// mux goroutine and UI calls.
type Service struct {
	service.Base

	mu      sync.Mutex
	ring    game.Ring
	board   Board
	ph      game.Phase
	winner  int8 // 0 none/abandoned, else winning seat+1
	draw    bool
	last    int16
	history []string
}

func New() *Service { return &Service{ring: game.NewRing(MaxSeats)} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.SetContext(ctx) }

// OnPromote resets host-only seat bookkeeping when this end is promoted to host
// (migration); a promoted host re-seeds Members from new joins.
func (s *Service) OnPromote() {
	s.mu.Lock()
	s.ring.OnPromote()
	s.mu.Unlock()
}

func (s *Service) MemberKeyed(id wire.ParticipantID, _ session.Role) {
	if !s.Ctx().Host {
		return
	}
	s.mu.Lock()
	s.ring.NoteKeyed(id) // every keyed member is seatable (up to MaxSeats)
	s.mu.Unlock()
}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	winnerSeat, over := s.ring.NoteLeft(id, s.ph)
	if over {
		s.endLocked(winnerSeat)
	}
	s.mu.Unlock()
	if over {
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
	if err := s.ring.AuthorizeStart(s.Ctx().Host, from, s.Ctx().Self, s.ph); err != nil {
		s.mu.Unlock()
		return err
	}
	seats := s.ring.Seat(s.Ctx().Self)
	turn := s.ring.Turn
	s.resetLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindNewGame, Seats: idsToU32(seats), Turn: uint8(turn)})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// resetLocked clears the board for a new game; the ring's Seats/Turn/Gone were
// set by Seat (host) or SetSeats (joiner) just before.
func (s *Service) resetLocked() {
	s.board = Board{}
	s.ph = game.Playing
	s.winner = 0
	s.draw = false
	s.last = -1
	s.history = nil
}

func (s *Service) notePlaceLocked(seat int, row, col int8) {
	glyph := "⚫"
	if seat >= 0 && seat < MaxSeats {
		glyph = stoneGlyph[seat]
	}
	s.history = append(s.history, fmt.Sprintf("%s %c%d", glyph, 'a'+col, row+1))
}

// Place plays the local player's stone at (row, col).
func (s *Service) Place(row, col int8) error {
	s.mu.Lock()
	seat, err := s.checkTurnLocked(s.Ctx().Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if _, err := s.placeLocked(seat, row, col); err != nil {
		s.mu.Unlock()
		return err
	}
	hash := s.applyAndHashLocked()
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

// placeLocked writes the stone and records notation; shared by send/receive.
func (s *Service) placeLocked(seat int, row, col int8) (int16, error) {
	idx, err := s.board.Place(row, col, int8(seat)+1)
	if err != nil {
		return 0, err
	}
	s.last = idx
	s.notePlaceLocked(seat, row, col)
	return idx, nil
}

// Resign forfeits the local player's seat; the game ends (sole survivor wins,
// otherwise abandoned).
func (s *Service) Resign() error {
	s.mu.Lock()
	if _, seated := s.ring.SideOf(s.Ctx().Self); !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return ErrNoResign
	}
	winnerSeat, over := s.ring.Concede(s.Ctx().Self)
	if over {
		s.endLocked(winnerSeat)
	}
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
		return fmt.Errorf("gomokup: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		if from != s.Ctx().HostID {
			return fmt.Errorf("gomokup: new game from non-host %d", from)
		}
		s.mu.Lock()
		s.ring.SetSeats(u32ToIDs(m.Seats), int(m.Turn))
		s.resetLocked()
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
	case kindResign:
		return s.handleResign(from)
	}
	return fmt.Errorf("gomokup: unknown message kind %d", m.Kind)
}

func (s *Service) handlePlace(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	seat, err := s.checkTurnLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if _, err := s.placeLocked(seat, m.Row, m.Col); err != nil {
		s.mu.Unlock()
		return err
	}
	hash := s.applyAndHashLocked()
	ok := bytes.Equal(hash, m.StateHash)
	if !ok {
		s.ph = game.Over
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("gomokup: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	if _, seated := s.ring.SideOf(from); !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return nil
	}
	winnerSeat, over := s.ring.Concede(from)
	if over {
		s.endLocked(winnerSeat)
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

// endLocked marks the game over with the given winning seat (-1 = abandoned).
func (s *Service) endLocked(winnerSeat int) {
	s.ph = game.Over
	if winnerSeat >= 0 {
		s.winner = int8(winnerSeat) + 1
	} else {
		s.winner = 0
	}
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ph == game.Idle {
		return nil, nil
	}
	return wire.Marshal(snapshot{
		Board: s.board, Seats: idsToU32(s.ring.Seats), Turn: uint8(s.ring.Turn),
		Phase: uint8(s.ph), Winner: s.winner, Draw: s.draw, Last: s.last,
		History: s.history, Gone: s.ring.Gone,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("gomokup: restore: %w", err)
	}
	s.mu.Lock()
	if s.ph != game.Idle { // late-joiner catch-up only
		s.mu.Unlock()
		return nil
	}
	s.board = snap.Board
	s.ring.SetSeats(u32ToIDs(snap.Seats), int(snap.Turn))
	if len(snap.Gone) == len(s.ring.Gone) {
		copy(s.ring.Gone, snap.Gone)
	}
	s.ph = game.Phase(snap.Phase)
	s.winner = snap.Winner
	s.draw = snap.Draw
	s.last = snap.Last
	s.history = snap.History
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

func (s *Service) checkTurnLocked(who wire.ParticipantID) (int, error) {
	if s.ph != game.Playing {
		return 0, ErrNoGame
	}
	seat, seated := s.ring.SideOf(who)
	if !seated {
		return 0, ErrNotSeat
	}
	if seat != s.ring.Turn {
		return 0, ErrNotTurn
	}
	return seat, nil
}

// applyAndHashLocked resolves the outcome after a placement, then hashes the
// post-move state — computed identically on send and receive.
func (s *Service) applyAndHashLocked() []byte {
	if w, _ := s.board.Winner(); w != 0 {
		s.winner = w
		s.ph = game.Over
	} else if s.board.Full() {
		s.draw = true
		s.ph = game.Over
	} else {
		s.ring.NextTurn()
	}
	b, err := wire.Marshal(struct {
		Board Board `cbor:"1,keyasint"`
		Turn  uint8 `cbor:"2,keyasint"`
		Phase uint8 `cbor:"3,keyasint"`
	}{s.board, uint8(s.ring.Turn), uint8(s.ph)})
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
		Playing: true,
		Board:   s.board,
		Seats:   append([]wire.ParticipantID(nil), s.ring.Seats...),
		Gone:    append([]bool(nil), s.ring.Gone...),
		Last:    s.last,
		History: append([]string(nil), s.history...),
	}
	switch {
	case s.ph == game.Over && s.draw:
		st.Outcome = "draw"
	case s.ph == game.Over && s.winner > 0:
		st.WinnerID = s.ring.IDOf(int(s.winner) - 1)
		st.Outcome = fmt.Sprintf("seat %d wins", s.winner)
	case s.ph == game.Over:
		st.Outcome = "abandoned"
	default:
		st.TurnID = s.ring.IDOf(s.ring.Turn)
	}
	if _, cells := s.board.Winner(); cells != nil {
		st.WinCells = cells
	}
	return st
}

func idsToU32(ids []wire.ParticipantID) []uint32 {
	out := make([]uint32, len(ids))
	for i, id := range ids {
		out[i] = uint32(id)
	}
	return out
}

func u32ToIDs(us []uint32) []wire.ParticipantID {
	out := make([]wire.ParticipantID, len(us))
	for i, u := range us {
		out[i] = wire.ParticipantID(u)
	}
	return out
}
