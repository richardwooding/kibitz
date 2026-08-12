package game

import (
	"errors"
	"testing"

	"github.com/richardwooding/kibitz/internal/proto"
)

func TestSeats(t *testing.T) {
	s := Seats{P1: 1, P2: 2}
	if side, ok := s.SideOf(1); !ok || side != P1 {
		t.Fatalf("SideOf(1) = %v %v", side, ok)
	}
	if side, ok := s.SideOf(2); !ok || side != P2 {
		t.Fatalf("SideOf(2) = %v %v", side, ok)
	}
	if _, ok := s.SideOf(3); ok {
		t.Fatal("spectator got a seat")
	}
	if s.IDOf(P1) != 1 || s.IDOf(P2) != 2 {
		t.Fatal("IDOf mismatch")
	}
}

func TestNoteKeyedRecordsFirstPlayerOnly(t *testing.T) {
	var tb Table
	tb.NoteKeyed(5, proto.RoleSpectator)
	if tb.Opponent != 0 {
		t.Fatal("spectator recorded as opponent")
	}
	tb.NoteKeyed(2, proto.RolePlayer)
	tb.NoteKeyed(3, proto.RolePlayer) // shouldn't happen, but must not displace
	if tb.Opponent != 2 {
		t.Fatalf("opponent = %d", tb.Opponent)
	}
}

func TestAuthorizeStart(t *testing.T) {
	var tb Table
	if err := tb.AuthorizeStart(false, 2, 1, Idle); !errors.Is(err, ErrNotAuthority) {
		t.Fatalf("non-host: %v", err)
	}
	if err := tb.AuthorizeStart(true, 1, 1, Idle); !errors.Is(err, ErrNoOpponent) {
		t.Fatalf("no opponent: %v", err)
	}
	tb.NoteKeyed(2, proto.RolePlayer)
	if err := tb.AuthorizeStart(true, 1, 1, Playing); !errors.Is(err, ErrInProgress) {
		t.Fatalf("mid-game: %v", err)
	}
	if err := tb.AuthorizeStart(true, 3, 1, Idle); !errors.Is(err, ErrNotSeated) {
		t.Fatalf("spectator start: %v", err)
	}
	if err := tb.AuthorizeStart(true, 1, 1, Idle); err != nil {
		t.Fatalf("host start: %v", err)
	}
	if err := tb.AuthorizeStart(true, 2, 1, Over); err != nil {
		t.Fatalf("player rematch: %v", err)
	}
}

func TestNextSeatsAlternates(t *testing.T) {
	var tb Table
	tb.NoteKeyed(2, proto.RolePlayer)
	s1 := tb.NextSeats(1)
	if s1 != (Seats{P1: 1, P2: 2}) {
		t.Fatalf("game 1 seats %+v", s1)
	}
	s2 := tb.NextSeats(1)
	if s2 != (Seats{P1: 2, P2: 1}) {
		t.Fatalf("game 2 seats %+v (rematch must swap)", s2)
	}
	s3 := tb.NextSeats(1)
	if s3 != s1 {
		t.Fatalf("game 3 seats %+v", s3)
	}
}

// A leave can overtake the newGame that seats the leaver (the relay orders
// frames per sender only). NoteLeft then sees no seat — ApplyDeparted must
// report the forfeit once the stale seat pair lands.
func TestApplyDepartedForfeitsRacedSeats(t *testing.T) {
	var tb Table
	tb.NoteKeyed(2, proto.RolePlayer)
	if _, forfeit := tb.NoteLeft(2, Idle); forfeit {
		t.Fatal("pre-seat leave forfeited")
	}
	// Seats land from the host's newGame frame, still naming the leaver
	// (exactly how joiners install them — NextSeats is host-side only).
	tb.Seats = Seats{P1: 1, P2: 2}
	winner, forfeit := tb.ApplyDeparted()
	if !forfeit || winner != P1 {
		t.Fatalf("forfeit=%v winner=%v, want true,P1", forfeit, winner)
	}
	// No departures among the seated pair: no forfeit.
	tb.Seats = Seats{P1: 1, P2: 3}
	if _, forfeit := tb.ApplyDeparted(); forfeit {
		t.Fatal("forfeit without a departed seat")
	}
}

func TestNoteLeftForfeit(t *testing.T) {
	var tb Table
	tb.NoteKeyed(2, proto.RolePlayer)
	tb.NextSeats(1)

	// Spectator leaving mid-game: no forfeit.
	if _, forfeit := tb.NoteLeft(9, Playing); forfeit {
		t.Fatal("spectator leave forfeited")
	}
	// Seated player leaving mid-game: opponent wins.
	winner, forfeit := tb.NoteLeft(2, Playing)
	if !forfeit || winner != P1 {
		t.Fatalf("forfeit=%v winner=%v", forfeit, winner)
	}
	if tb.Opponent != 0 {
		t.Fatal("departed opponent still recorded")
	}
	// Player leaving when idle: no forfeit.
	tb.NoteKeyed(3, proto.RolePlayer)
	if _, forfeit := tb.NoteLeft(3, Idle); forfeit {
		t.Fatal("idle leave forfeited")
	}
}
