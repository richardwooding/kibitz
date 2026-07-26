package game

import "github.com/richardwooding/kibitz/internal/wire"

// Ring is the N-player turn runtime — an ordered seat list with a rotating turn
// — for games seating 2..Max players. It is the multi-seat sibling of Table
// (which stays two-player); the twelve existing games keep using Table. Logic
// only — no Sender, no mutex, no I/O; callers hold their own lock.
//
// Seating (who sits, in what order) is decided by the owning service's lobby
// (host-authoritative) and installed here via SetSeats; the twelve two-player
// games keep using Table. Ring only tracks the live game: whose turn it is,
// which seats have left, and rotation.
type Ring struct {
	Seats []wire.ParticipantID // seated players this game; index = seat number
	Gone  []bool               // per-seat: left/resigned mid-game (turn skips it)
	Turn  int                  // index into Seats whose turn it is
	Max   int                  // seat cap
	Games int                  // completed games; rotates who opens each rematch
}

// NewRing returns a Ring that seats up to max players.
func NewRing(max int) Ring { return Ring{Max: max} }

// SetSeats installs the seat list + opening turn for a new game (from the
// host's authoritative Begin). Gone is cleared to match a fresh game.
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

// NoteLeft handles a departure during a live game: a seated player's leave ENDS
// the game and marks its seat Gone (mirrors the two-player forfeit; avoids
// skipping a Gone seat mid-play, which would make turn advancement depend on
// move/leave ordering that has no global order across ends). Returns
// (winnerSeat, true) with the sole survivor, or (-1, true) when two or more
// remain (abandoned). A non-playing or non-seated leave returns (-1, false).
func (r *Ring) NoteLeft(id wire.ParticipantID, ph Phase) (int, bool) {
	if ph != Playing {
		return -1, false
	}
	return r.markGone(id)
}

// Concede ends a live game for a seated player who voluntarily forfeits — same
// end-of-game semantics as a mid-game leave.
func (r *Ring) Concede(id wire.ParticipantID) (int, bool) {
	return r.markGone(id)
}

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

// OnPromote resets rematch bookkeeping when this end is promoted to host
// (migration).
func (r *Ring) OnPromote() { r.Games = 0 }
