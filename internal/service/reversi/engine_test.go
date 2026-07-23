package reversi

import (
	"errors"
	"testing"
)

func TestOpeningMoves(t *testing.T) {
	b := Start()
	legal := LegalSquares(b, 1)
	// Black's four classic openings: d3(19), c4(26), f5(37), e6(44).
	want := map[int8]bool{19: true, 26: true, 37: true, 44: true}
	if len(legal) != 4 {
		t.Fatalf("black opening moves = %v", legal)
	}
	for _, sq := range legal {
		if !want[sq] {
			t.Fatalf("unexpected opening square %d", sq)
		}
	}
}

func TestPlaceFlipsAllDirections(t *testing.T) {
	// Black at d3 (19) flips d4 (27).
	b := Start()
	nb, err := Place(b, 1, 19)
	if err != nil {
		t.Fatal(err)
	}
	if nb[27] != 1 {
		t.Fatalf("d4 not flipped: %d", nb[27])
	}
	black, white := Counts(nb)
	if black != 4 || white != 1 {
		t.Fatalf("counts %d/%d, want 4/1", black, white)
	}

	// Multi-direction flip: construct a cross around an empty center.
	var c Board
	c[27] = 0
	// lines: left c(24..26)=white with black at 24? Build: place black at 27,
	// flipping horizontal (26 white, 25 black) and vertical (19 white, 11 black).
	c[26], c[25] = -1, 1
	c[19], c[11] = -1, 1
	nc, err := Place(c, 1, 27)
	if err != nil {
		t.Fatal(err)
	}
	if nc[26] != 1 || nc[19] != 1 {
		t.Fatalf("multi-direction flip failed: %d %d", nc[26], nc[19])
	}
}

func TestIllegalPlacements(t *testing.T) {
	b := Start()
	if _, err := Place(b, 1, 27); !errors.Is(err, ErrIllegalPlace) {
		t.Fatalf("occupied square: %v", err)
	}
	if _, err := Place(b, 1, 0); !errors.Is(err, ErrIllegalPlace) {
		t.Fatalf("no-flip square: %v", err)
	}
	if _, err := Place(b, 1, 99); !errors.Is(err, ErrIllegalPlace) {
		t.Fatalf("out of range: %v", err)
	}
}

func TestAdvanceNormalAlternation(t *testing.T) {
	b := Start()
	nb, _ := Place(b, 1, 19)
	next, passed, over := Advance(nb, 1)
	if next != -1 || passed || over {
		t.Fatalf("advance: next=%d passed=%v over=%v", next, passed, over)
	}
}

func TestAdvanceComputedPass(t *testing.T) {
	// White has no move; black does → black moves again with passed=true.
	// Tiny position: black line ... construct: black at 0, white at 1,
	// empty 2 → black can place at 2? 0=black,1=white,2=empty: black flips
	// 1 by placing at 2. White's options: needs a black disc flanked by
	// white — none. So after BLACK just moved (justMoved=1), white has no
	// move, black has one → pass back to black.
	var b Board
	b[0], b[1] = 1, -1
	next, passed, over := Advance(b, 1)
	if next != 1 || !passed || over {
		t.Fatalf("pass: next=%d passed=%v over=%v", next, passed, over)
	}
}

func TestAdvanceGameOver(t *testing.T) {
	// Nobody can move: single black disc.
	var b Board
	b[0] = 1
	_, _, over := Advance(b, 1)
	if !over {
		t.Fatal("game not over with no moves for either side")
	}

	// Full board is over too.
	var f Board
	for i := range f {
		f[i] = 1
	}
	if _, _, over := Advance(f, 1); !over {
		t.Fatal("full board not over")
	}
}

func TestFullRandomGameTerminates(t *testing.T) {
	// Deterministic playout: always the first legal square. Must terminate
	// with all discs accounted for and Advance reporting over.
	b := Start()
	side := int8(1)
	for turn := 0; ; turn++ {
		if turn > 200 {
			t.Fatal("game did not terminate")
		}
		legal := LegalSquares(b, side)
		if len(legal) == 0 {
			t.Fatalf("mover with no legal squares reached the loop (turn %d)", turn)
		}
		var err error
		b, err = Place(b, side, legal[0])
		if err != nil {
			t.Fatal(err)
		}
		next, _, over := Advance(b, side)
		if over {
			black, white := Counts(b)
			if black+white == 0 {
				t.Fatal("empty board at game end")
			}
			return
		}
		side = next
	}
}
