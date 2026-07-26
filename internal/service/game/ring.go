package game

import (
	"errors"

	"github.com/richardwooding/kibitz/internal/wire"
)

// ErrNotEnoughPlayers is returned when a multi-seat game is started with fewer
// than two participants seated.
var ErrNotEnoughPlayers = errors.New("game: need at least two players to start")

// Ring is the N-player seat/turn model — an ordered seat list with a rotating
// turn — for games seating 2..Max players. It is the multi-seat sibling of
// Table (which stays two-player); the twelve existing games keep using Table.
// Logic only — no Sender, no mutex, no I/O; callers hold their own lock.
//
// Seating is host-authoritative: only the host accumulates Members (MemberKeyed
// fires host-side), builds the seat list at Start via Seat, and broadcasts it;
// every other end adopts that list verbatim via SetSeats, so no end diverges.
type Ring struct {
	Members []wire.ParticipantID // host-side: every keyed participant, in key order
	Seats   []wire.ParticipantID // seated players this game; index = seat number
	Gone    []bool               // per-seat: left mid-game (stones stay, turn skips it)
	Turn    int                  // index into Seats whose turn it is
	Max     int                  // seat cap
	Games   int                  // completed games; rotates who opens each rematch
}

// NewRing returns a Ring that seats up to max players.
func NewRing(max int) Ring { return Ring{Max: max} }

// NoteKeyed records a participant available to be seated (host side). It keeps
// EVERY keyed member regardless of session role — a multiplayer game seats all
// present up to Max, and a role of Spectator here means "couldn't get the lone
// two-player seat", not necessarily "wants to watch". Deduped, key order.
func (r *Ring) NoteKeyed(id wire.ParticipantID) {
	for _, m := range r.Members {
		if m == id {
			return
		}
	}
	r.Members = append(r.Members, id)
}

// AuthorizeStart validates a start attempt: host authority only, not already in
// progress, and at least one other member present (host + 1 = two players). A
// non-host starter must be a known member (its startReq is relayed by the host).
func (r *Ring) AuthorizeStart(host bool, from, hostID wire.ParticipantID, ph Phase) error {
	if !host {
		return ErrNotAuthority
	}
	if ph == Playing {
		return ErrInProgress
	}
	if len(r.Members) < 1 {
		return ErrNotEnoughPlayers
	}
	if from != hostID && !r.isMember(from) {
		return ErrNotSeated
	}
	return nil
}

func (r *Ring) isMember(id wire.ParticipantID) bool {
	for _, m := range r.Members {
		if m == id {
			return true
		}
	}
	return false
}

// Seat builds the seat list for the next game (host side): the host first, then
// members in key order, truncated to Max. It rotates who opens (Turn = Games %
// n), resets the Gone flags, and bumps the games counter. Returns the seats to
// broadcast.
func (r *Ring) Seat(hostID wire.ParticipantID) []wire.ParticipantID {
	seats := make([]wire.ParticipantID, 0, r.Max)
	seats = append(seats, hostID)
	for _, m := range r.Members {
		if len(seats) >= r.Max {
			break
		}
		seats = append(seats, m)
	}
	r.Seats = seats
	r.Gone = make([]bool, len(seats))
	r.Turn = r.Games % len(seats)
	r.Games++
	return seats
}

// SetSeats installs an authoritative seat list + opening turn received from the
// host (non-host ends). Gone is cleared to match a fresh game.
func (r *Ring) SetSeats(seats []wire.ParticipantID, turn int) {
	r.Seats = append([]wire.ParticipantID(nil), seats...)
	r.Gone = make([]bool, len(seats))
	if turn < 0 || turn >= len(seats) {
		turn = 0
	}
	r.Turn = turn
}

// SideOf reports which seat a participant holds this game.
func (r *Ring) SideOf(id wire.ParticipantID) (int, bool) {
	for i, s := range r.Seats {
		if s == id {
			return i, true
		}
	}
	return 0, false
}

// IDOf returns the participant in a seat (0 if out of range).
func (r *Ring) IDOf(seat int) wire.ParticipantID {
	if seat < 0 || seat >= len(r.Seats) {
		return 0
	}
	return r.Seats[seat]
}

// NextTurn advances the turn to the next still-active (not Gone) seat.
func (r *Ring) NextTurn() {
	for i := 0; i < len(r.Seats); i++ {
		r.Turn = (r.Turn + 1) % len(r.Seats)
		if !r.Gone[r.Turn] {
			return
		}
	}
}

// ActiveCount is how many seated players have not left.
func (r *Ring) ActiveCount() int {
	n := 0
	for _, g := range r.Gone {
		if !g {
			n++
		}
	}
	return n
}

func (r *Ring) lastActive() int {
	for i, g := range r.Gone {
		if !g {
			return i
		}
	}
	return 0
}

// NoteLeft handles a departure. Pre-game it drops the leaver from the member
// pool. During a live game a SEATED player's departure ENDS the game and marks
// its seat Gone. This mirrors the two-player forfeit and — deliberately —
// avoids skipping a Gone seat mid-play, which would make turn advancement depend
// on move/leave ordering that has no global order across ends (a false-desync
// risk). Returns (winnerSeat, true) with the sole surviving seat, or (-1, true)
// when two or more remain (an abandoned game, no single winner). A pre-game or
// non-seated leave returns (-1, false).
func (r *Ring) NoteLeft(id wire.ParticipantID, ph Phase) (int, bool) {
	r.dropMember(id)
	if ph != Playing {
		return -1, false
	}
	return r.markGone(id)
}

// Concede ends a live game for a seated player who voluntarily forfeits (resign)
// WITHOUT removing them from the member pool, so they can be seated in a
// rematch. Same end-of-game semantics as a mid-game leave.
func (r *Ring) Concede(id wire.ParticipantID) (int, bool) {
	return r.markGone(id)
}

// markGone marks a seated player's seat Gone and reports the outcome: the sole
// surviving seat wins (winnerSeat, true), two-or-more remaining ends abandoned
// (-1, true), and a non-seated/already-gone id is a no-op (-1, false).
func (r *Ring) markGone(id wire.ParticipantID) (int, bool) {
	seat, seated := r.SideOf(id)
	if !seated || r.Gone[seat] {
		return -1, false
	}
	r.Gone[seat] = true
	if r.ActiveCount() == 1 {
		return r.lastActive(), true
	}
	return -1, true
}

func (r *Ring) dropMember(id wire.ParticipantID) {
	for i, m := range r.Members {
		if m == id {
			r.Members = append(r.Members[:i], r.Members[i+1:]...)
			return
		}
	}
}

// OnPromote resets host-only bookkeeping when this end is promoted to host
// (migration). A promoted host holds no accumulated members, so a fresh game
// would re-seed from new joins; the in-progress game keeps its Seats and is
// unaffected (departures are handled by NoteLeft on every end).
func (r *Ring) OnPromote() {
	r.Members = nil
	r.Games = 0
}
