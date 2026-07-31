package game

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/wire"
)

func pid(n uint32) wire.ParticipantID { return wire.ParticipantID(n) }

func TestRingSetSeatsAndLookup(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 1)
	if len(r.Seats) != 3 || r.Turn != 1 {
		t.Fatalf("seats=%v turn=%d, want 3 seats, turn 1", r.Seats, r.Turn)
	}
	side, ok := r.SideOf(pid(3))
	if !ok || side != 2 {
		t.Fatalf("SideOf(3)=%d,%v want 2,true", side, ok)
	}
	if r.IDOf(0) != pid(1) {
		t.Fatalf("IDOf(0)=%d want 1", r.IDOf(0))
	}
	// Out-of-range turn is clamped to 0.
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 9)
	if r.Turn != 0 {
		t.Fatalf("turn=%d, want clamped 0", r.Turn)
	}
}

func TestRingNextTurnSkipsGone(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	r.Gone[1] = true // seat 1 left
	r.NextTurn()     // 0 -> skip 1 -> 2
	if r.Turn != 2 {
		t.Fatalf("turn=%d, want 2 (skipped gone seat 1)", r.Turn)
	}
	r.NextTurn() // 2 -> wrap to 0
	if r.Turn != 0 {
		t.Fatalf("turn=%d, want 0 (wrapped)", r.Turn)
	}
}

func TestRingNoteLeftForfeitWhenOneActive(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 0)
	winner, over := r.NoteLeft(pid(2), Playing)
	if !over || winner != 0 {
		t.Fatalf("2-player leave: winner=%d over=%v, want 0,true", winner, over)
	}
}

func TestRingNoteLeftEndsGameWithThree(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 1)
	winner, over := r.NoteLeft(pid(2), Playing)
	if !over || winner != -1 {
		t.Fatalf("3-player leave: winner=%d over=%v, want -1,true (abandoned)", winner, over)
	}
	if !r.Gone[1] {
		t.Fatal("departed seat 1 should be marked Gone")
	}
	if r.ActiveCount() != 2 {
		t.Fatalf("active=%d, want 2", r.ActiveCount())
	}
}

func TestRingNoteLeftIgnoredWhenNotPlaying(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 0)
	if winner, over := r.NoteLeft(pid(2), Idle); over || winner != -1 {
		t.Fatalf("leave while idle: %d,%v, want -1,false", winner, over)
	}
}

func TestRingConcede(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	if winner, over := r.Concede(pid(1)); !over || winner != -1 {
		t.Fatalf("concede with 3: %d,%v, want -1,true", winner, over)
	}
	if winner, over := r.Concede(pid(2)); !over || winner != 2 {
		t.Fatalf("concede down to one: %d,%v, want seat 2,true", winner, over)
	}
}

// A leave can overtake the newGame that seats the leaver (the relay orders
// frames per sender only). NoteLeft then sees no seat — ApplyDeparted must
// end the game once the stale seat list lands.
func TestRingApplyDepartedEndsRacedNewGame(t *testing.T) {
	r := NewRing(4)
	if winner, over := r.NoteLeft(pid(3), Idle); over || winner != -1 {
		t.Fatalf("pre-seat leave: %d,%v, want -1,false", winner, over)
	}
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	winner, over := r.ApplyDeparted()
	if !over || winner != -1 {
		t.Fatalf("3-player with departed seat: %d,%v, want -1,true (abandoned)", winner, over)
	}
	if !r.Gone[2] || r.Gone[0] || r.Gone[1] {
		t.Fatalf("gone=%v, want only seat 2 marked", r.Gone)
	}
}

func TestRingApplyDepartedSoleSurvivorWins(t *testing.T) {
	r := NewRing(4)
	r.NoteLeft(pid(2), Idle)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 0)
	if winner, over := r.ApplyDeparted(); !over || winner != 0 {
		t.Fatalf("2-player with departed seat: %d,%v, want 0,true", winner, over)
	}
}

func TestRingApplyDepartedNoopWithoutDepartures(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	if winner, over := r.ApplyDeparted(); over || winner != -1 {
		t.Fatalf("no departures: %d,%v, want -1,false", winner, over)
	}
	// Already-Gone seats are not re-marked: a second pass reports nothing new.
	r.NoteLeft(pid(3), Playing)
	if _, over := r.ApplyDeparted(); over {
		t.Fatal("second pass re-marked an already-gone seat")
	}
}

func TestRingOnPromoteResetsGames(t *testing.T) {
	r := NewRing(4)
	r.Games = 3
	r.OnPromote()
	if r.Games != 0 {
		t.Fatalf("Games=%d after promote, want 0", r.Games)
	}
}
