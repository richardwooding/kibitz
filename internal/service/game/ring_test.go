package game

import (
	"errors"
	"testing"

	"github.com/richardwooding/kibitz/internal/wire"
)

func pid(n uint32) wire.ParticipantID { return wire.ParticipantID(n) }

func TestRingNoteKeyedDedup(t *testing.T) {
	r := NewRing(4)
	r.NoteKeyed(pid(2))
	r.NoteKeyed(pid(3))
	r.NoteKeyed(pid(2)) // dup ignored
	if len(r.Members) != 2 || r.Members[0] != pid(2) || r.Members[1] != pid(3) {
		t.Fatalf("members = %v, want [2 3] in key order", r.Members)
	}
}

func TestRingAuthorizeStart(t *testing.T) {
	r := NewRing(4)
	host := pid(1)
	if err := r.AuthorizeStart(false, pid(2), host, Idle); !errors.Is(err, ErrNotAuthority) {
		t.Fatalf("non-host: %v, want ErrNotAuthority", err)
	}
	if err := r.AuthorizeStart(true, host, host, Idle); !errors.Is(err, ErrNotEnoughPlayers) {
		t.Fatalf("no members: %v, want ErrNotEnoughPlayers", err)
	}
	r.NoteKeyed(pid(2))
	if err := r.AuthorizeStart(true, host, host, Playing); !errors.Is(err, ErrInProgress) {
		t.Fatalf("in progress: %v, want ErrInProgress", err)
	}
	if err := r.AuthorizeStart(true, pid(9), host, Idle); !errors.Is(err, ErrNotSeated) {
		t.Fatalf("stranger start: %v, want ErrNotSeated", err)
	}
	if err := r.AuthorizeStart(true, host, host, Idle); err != nil {
		t.Fatalf("host with one member: %v, want nil", err)
	}
	if err := r.AuthorizeStart(true, pid(2), host, Idle); err != nil {
		t.Fatalf("member start (relayed by host): %v, want nil", err)
	}
}

func TestRingSeatAndRotation(t *testing.T) {
	r := NewRing(4)
	host := pid(1)
	for _, m := range []uint32{2, 3, 4, 5} { // 5 members, Max 4 → host + first 3
		r.NoteKeyed(pid(m))
	}
	seats := r.Seat(host)
	if len(seats) != 4 {
		t.Fatalf("seats = %v, want 4 (host + 3)", seats)
	}
	if seats[0] != host || seats[1] != pid(2) || seats[3] != pid(4) {
		t.Fatalf("seat order = %v, want [1 2 3 4]", seats)
	}
	if r.Turn != 0 {
		t.Fatalf("first game opens on seat %d, want 0", r.Turn)
	}
	// Rematch rotates who opens.
	r.Seat(host)
	if r.Turn != 1 {
		t.Fatalf("second game opens on seat %d, want 1", r.Turn)
	}

	side, ok := r.SideOf(pid(3))
	if !ok || side != 2 {
		t.Fatalf("SideOf(3) = %d,%v, want 2,true", side, ok)
	}
	if r.IDOf(2) != pid(3) {
		t.Fatalf("IDOf(2) = %d, want 3", r.IDOf(2))
	}
}

func TestRingNextTurnSkipsGone(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	r.Gone[1] = true // seat 1 left
	r.NextTurn()     // 0 -> skip 1 -> 2
	if r.Turn != 2 {
		t.Fatalf("turn = %d, want 2 (skipped gone seat 1)", r.Turn)
	}
	r.NextTurn() // 2 -> wrap to 0
	if r.Turn != 0 {
		t.Fatalf("turn = %d, want 0 (wrapped)", r.Turn)
	}
}

func TestRingNoteLeftForfeitWhenOneActive(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 0)
	winner, forfeit := r.NoteLeft(pid(2), Playing)
	if !forfeit || winner != 0 {
		t.Fatalf("2-player leave: winner=%d forfeit=%v, want 0,true", winner, forfeit)
	}
}

func TestRingNoteLeftEndsGameWithThree(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 1)
	winner, over := r.NoteLeft(pid(2), Playing) // a seated player leaves
	if !over || winner != -1 {
		t.Fatalf("3-player leave: winner=%d over=%v, want -1,true (abandoned, no winner)", winner, over)
	}
	if !r.Gone[1] {
		t.Fatal("departed seat 1 should be marked Gone")
	}
	if r.ActiveCount() != 2 {
		t.Fatalf("active = %d, want 2", r.ActiveCount())
	}
}

func TestRingNoteLeftPreGameDropsMember(t *testing.T) {
	r := NewRing(4)
	r.NoteKeyed(pid(2))
	r.NoteKeyed(pid(3))
	winner, over := r.NoteLeft(pid(2), Idle)
	if over || winner != -1 {
		t.Fatalf("pre-game leave: %d,%v, want -1,false", winner, over)
	}
	if len(r.Members) != 1 || r.Members[0] != pid(3) {
		t.Fatalf("members = %v, want [3]", r.Members)
	}
}

func TestRingOnPromoteResets(t *testing.T) {
	r := NewRing(4)
	r.NoteKeyed(pid(2))
	r.Games = 3
	r.OnPromote()
	if len(r.Members) != 0 || r.Games != 0 {
		t.Fatalf("after promote: members=%v games=%d, want empty/0", r.Members, r.Games)
	}
}
