// The Gomoku Party service — five-in-a-row for 2–4 players on a shared 19×19
// board with rotating turns. Same both-sides-validate shape as the two-player
// gomoku/connect4 services (a move is a free (row,col) placement; a position
// hash is computed identically on send and receive), but backed by game.Ring
// (N ordered seats + a rotating turn) instead of the two-seat game.Table.
//
// Seating goes through a take-a-seat / watch LOBBY: the host opens a table, any
// participant (player or spectator) claims or releases a seat (up to MaxSeats),
// and the host begins once ≥2 are seated. Seating is host-authoritative (the
// host owns the claimed list and broadcasts it; every end adopts it verbatim).
// Play is peer-to-peer both-sides-validate; a mover's color is derived from its
// seat, never trusted from the wire.
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
	kindNewGame  uint8 = 1 // host→all: begin play with a seat list + opening turn
	kindStartReq uint8 = 2 // non-host→host: please open a table
	kindPlace    uint8 = 3
	kindResign   uint8 = 4
	kindOpen     uint8 = 5 // host→all: a table is open for seating (carries claimed)
	kindSeats    uint8 = 6 // host→all: authoritative claimed-seat list
	kindSeatReq  uint8 = 7 // participant→host: take a seat
	kindLeaveReq uint8 = 8 // participant→host: leave my seat
)

type msg struct {
	Kind      uint8    `cbor:"1,keyasint"`
	Seats     []uint32 `cbor:"2,keyasint,omitempty"` // seat/claimed list
	Turn      uint8    `cbor:"3,keyasint,omitempty"` // opening seat index (begin)
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
	Lobby   bool     `cbor:"10,keyasint,omitempty"`
	Claimed []uint32 `cbor:"11,keyasint,omitempty"`
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing  bool
	Lobby    bool // in the take-a-seat lobby (not yet playing)
	Board    Board
	Seats    []wire.ParticipantID // lobby: claimed seats; playing: the seats (index = color-1)
	Gone     []bool               // per-seat: left/resigned (playing)
	TurnID   wire.ParticipantID   // 0 when over/idle/lobby
	WinnerID wire.ParticipantID   // 0 = none/abandoned/draw
	Draw     bool
	Outcome  string // "", "seat N wins", "abandoned", "draw" (UI shows names)
	WinCells []int16
	Last     int16    // last placed stone's index, -1 when none
	History  []string // ordered notation, "🔴 h8"
	MaxSeats int      // seat cap
	CanBegin bool     // this end is the host and ≥2 are seated
}

// stoneGlyph is the notation marker per seat color (index 0..MaxSeats-1).
var stoneGlyph = [MaxSeats]string{"🔴", "🔵", "🟢", "🟡"}

var (
	ErrNotTurn  = errors.New("gomokup: not your turn")
	ErrNoGame   = errors.New("gomokup: no game in progress")
	ErrNotSeat  = errors.New("gomokup: not a player in this game")
	ErrNoResign = errors.New("gomokup: no game to resign")

	errNotHost    = errors.New("gomokup: only the host can do that")
	errNotLobby   = errors.New("gomokup: no open table")
	errTooFew     = errors.New("gomokup: need at least two seated players")
	errInProgress = errors.New("gomokup: a game is in progress")
)

// Service implements service.Service; the mutex covers game state between the
// mux goroutine and UI calls.
type Service struct {
	service.Base

	mu      sync.Mutex
	ring    game.Ring
	lobby   bool                 // an open table awaiting seats
	claimed []wire.ParticipantID // host-authoritative claimed seats (mirrored on all ends)
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

// OnPromote resets rematch bookkeeping when this end is promoted to host.
func (s *Service) OnPromote() {
	s.mu.Lock()
	s.ring.OnPromote()
	s.mu.Unlock()
}

// MemberKeyed is a no-op: seating is by explicit lobby claim, not by keying.
// (Kept to satisfy the MemberObserver interface alongside MemberLeft.)
func (s *Service) MemberKeyed(wire.ParticipantID, session.Role) {}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	host := s.Ctx().Host
	if host {
		s.claimed = removeID(s.claimed, id)
	}
	winnerSeat, over := s.ring.NoteLeft(id, s.ph)
	if over {
		s.endLocked(winnerSeat)
	}
	inLobby := s.lobby
	seats := idsToU32(s.claimed)
	s.mu.Unlock()

	switch {
	case over:
		s.emitState()
	case host && inLobby:
		_ = s.broadcastSeats(seats) // a claimant left the lobby → free the seat everywhere
	}
}

// --- lobby: open / seat / begin ---------------------------------------------

// Start opens a table (host) or asks the host to open one (non-host).
func (s *Service) Start() error {
	if !s.Ctx().Host {
		return s.sendToHost(kindStartReq)
	}
	return s.hostOpen()
}

// hostOpen enters the seating lobby. It carries the claimed list across a
// rematch (leavers already dropped); a fresh table seeds the host into seat 0.
func (s *Service) hostOpen() error {
	s.mu.Lock()
	if s.ph == game.Playing {
		s.mu.Unlock()
		return errInProgress
	}
	s.clearBoardLocked()
	s.ph = game.Idle
	s.lobby = true
	if len(s.claimed) == 0 {
		s.claimed = []wire.ParticipantID{s.Ctx().Self}
	}
	seats := idsToU32(s.claimed)
	s.mu.Unlock()
	return s.broadcast(kindOpen, seats)
}

// TakeSeat / LeaveSeat claim or release this end's seat in the open lobby.
func (s *Service) TakeSeat() error {
	if !s.Ctx().Host {
		return s.sendToHost(kindSeatReq)
	}
	return s.hostClaim(s.Ctx().Self)
}

func (s *Service) LeaveSeat() error {
	if !s.Ctx().Host {
		return s.sendToHost(kindLeaveReq)
	}
	return s.hostRelease(s.Ctx().Self)
}

func (s *Service) hostClaim(id wire.ParticipantID) error {
	s.mu.Lock()
	if !s.lobby {
		s.mu.Unlock()
		return errNotLobby
	}
	if len(s.claimed) < MaxSeats && !containsID(s.claimed, id) {
		s.claimed = append(s.claimed, id)
	}
	seats := idsToU32(s.claimed)
	s.mu.Unlock()
	return s.broadcastSeats(seats)
}

func (s *Service) hostRelease(id wire.ParticipantID) error {
	s.mu.Lock()
	if !s.lobby {
		s.mu.Unlock()
		return errNotLobby
	}
	s.claimed = removeID(s.claimed, id)
	seats := idsToU32(s.claimed)
	s.mu.Unlock()
	return s.broadcastSeats(seats)
}

// Begin starts play from the claimed seats (host only, ≥2 seated).
func (s *Service) Begin() error {
	s.mu.Lock()
	if !s.Ctx().Host {
		s.mu.Unlock()
		return errNotHost
	}
	if !s.lobby || len(s.claimed) < 2 {
		s.mu.Unlock()
		return errTooFew
	}
	seats := append([]wire.ParticipantID(nil), s.claimed...)
	turn := s.ring.Games % len(seats)
	s.ring.SetSeats(seats, turn)
	s.ring.Games++
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

// --- play -------------------------------------------------------------------

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
		return s.handleNewGame(from, m)
	case kindStartReq:
		if s.Ctx().Host {
			return s.hostOpen()
		}
		return nil
	case kindSeatReq:
		if s.Ctx().Host {
			return s.hostClaim(from)
		}
		return nil
	case kindLeaveReq:
		if s.Ctx().Host {
			return s.hostRelease(from)
		}
		return nil
	case kindOpen:
		return s.handleOpen(from, m)
	case kindSeats:
		return s.handleSeats(from, m)
	case kindPlace:
		return s.handlePlace(from, m)
	case kindResign:
		return s.handleResign(from)
	}
	return fmt.Errorf("gomokup: unknown message kind %d", m.Kind)
}

func (s *Service) handleNewGame(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return fmt.Errorf("gomokup: new game from non-host %d", from)
	}
	s.mu.Lock()
	s.ring.SetSeats(u32ToIDs(m.Seats), int(m.Turn))
	s.resetLocked()
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleOpen(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return nil
	}
	s.mu.Lock()
	s.clearBoardLocked()
	s.ph = game.Idle
	s.lobby = true
	s.claimed = u32ToIDs(m.Seats)
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleSeats(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return nil
	}
	s.mu.Lock()
	s.claimed = u32ToIDs(m.Seats)
	s.mu.Unlock()
	s.emitState()
	return nil
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
	s.lobby = false
	if winnerSeat >= 0 {
		s.winner = int8(winnerSeat) + 1
	} else {
		s.winner = 0
	}
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ph == game.Idle && !s.lobby {
		return nil, nil
	}
	return wire.Marshal(snapshot{
		Board: s.board, Seats: idsToU32(s.ring.Seats), Turn: uint8(s.ring.Turn),
		Phase: uint8(s.ph), Winner: s.winner, Draw: s.draw, Last: s.last,
		History: s.history, Gone: s.ring.Gone, Lobby: s.lobby, Claimed: idsToU32(s.claimed),
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("gomokup: restore: %w", err)
	}
	s.mu.Lock()
	if s.ph != game.Idle || s.lobby { // late-joiner catch-up only
		s.mu.Unlock()
		return nil
	}
	s.claimed = u32ToIDs(snap.Claimed)
	if snap.Lobby {
		s.lobby = true
		s.ph = game.Idle
		s.mu.Unlock()
		s.emitState()
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

// clearBoardLocked resets the board (not the phase or lobby flags).
func (s *Service) clearBoardLocked() {
	s.board = Board{}
	s.winner = 0
	s.draw = false
	s.last = -1
	s.history = nil
}

// resetLocked begins a fresh game: board cleared, playing, lobby closed. The
// ring's Seats/Turn/Gone were set just before (Begin host / handleNewGame joiner).
func (s *Service) resetLocked() {
	s.clearBoardLocked()
	s.ph = game.Playing
	s.lobby = false
}

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
	if s.ph == game.Idle && !s.lobby {
		return State{Last: -1, MaxSeats: MaxSeats}
	}
	st := State{MaxSeats: MaxSeats, Last: s.last, History: append([]string(nil), s.history...)}
	if s.lobby {
		st.Lobby = true
		st.Seats = append([]wire.ParticipantID(nil), s.claimed...)
		st.CanBegin = s.Ctx().Host && len(s.claimed) >= 2
		return st
	}
	st.Playing = true
	st.Board = s.board
	st.Seats = append([]wire.ParticipantID(nil), s.ring.Seats...)
	st.Gone = append([]bool(nil), s.ring.Gone...)
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

// --- message + slice helpers -------------------------------------------------

func (s *Service) sendToHost(kind uint8) error {
	body, err := wire.Marshal(msg{Kind: kind})
	if err != nil {
		return err
	}
	return s.Ctx().Send.SendTo(s.Ctx().HostID, ID, body)
}

func (s *Service) broadcast(kind uint8, seats []uint32) error {
	body, err := wire.Marshal(msg{Kind: kind, Seats: seats})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) broadcastSeats(seats []uint32) error { return s.broadcast(kindSeats, seats) }

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

func containsID(ids []wire.ParticipantID, id wire.ParticipantID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func removeID(ids []wire.ParticipantID, id wire.ParticipantID) []wire.ParticipantID {
	for i, x := range ids {
		if x == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}
