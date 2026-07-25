package dots

import "testing"

// drawn builds a Board with the given edge ids marked drawn (owners left empty —
// BestMove ignores ownership).
func drawn(edges ...int8) Board {
	var b Board
	for _, e := range edges {
		b.Edges[e] = 1
	}
	return b
}

// full returns a board with every edge drawn.
func full() Board {
	var b Board
	for e := range b.Edges {
		b.Edges[e] = 1
	}
	return b
}

func TestBestMoveTakesFreeBox(t *testing.T) {
	// box 0 has three sides drawn (top 0, bottom 5, left 30); the completing
	// edge is its right edge, 31.
	b := drawn(0, 5, 30)
	for pick := 0; pick < 5; pick++ {
		got, ok := BestMove(b, pick)
		if !ok {
			t.Fatalf("pick=%d: ok=false, want a free-box move", pick)
		}
		if got != 31 {
			t.Fatalf("pick=%d: got edge %d, want 31 (completes box 0)", pick, got)
		}
	}
}

func TestBestMoveNeverGiftsWhenSafeExists(t *testing.T) {
	// box 0 has two sides (top 0, left 30). Drawing its bottom (5) or right (31)
	// would make a third side; plenty of other edges are safe.
	b := drawn(0, 30)
	for pick := 0; pick < 60; pick++ {
		got, ok := BestMove(b, pick)
		if !ok {
			t.Fatalf("pick=%d: ok=false on an open board", pick)
		}
		if createsThirdSide(&b, got) {
			t.Fatalf("pick=%d: chose unsafe edge %d that creates a third side", pick, got)
		}
	}
}

func TestBestMovePicksSmallerChainWhenForced(t *testing.T) {
	// Dense board where every undrawn edge is unsafe (no safe move, no free box):
	//   row-0 vertical edges {30..35} form one long 5-box chain,
	//   corner box 20's two boundary edges {25,54} form a 1-box chain.
	// BestMove must open the small chain, i.e. return 25 or 54.
	b := full()
	undrawn := []int8{30, 31, 32, 33, 34, 35, 25, 54}
	for _, e := range undrawn {
		b.Edges[e] = 0
	}

	small := map[int8]bool{25: true, 54: true}
	sawSmall := false
	for pick := 0; pick < 24; pick++ {
		got, ok := BestMove(b, pick)
		if !ok {
			t.Fatalf("pick=%d: ok=false, want a forced move", pick)
		}
		if !small[got] {
			t.Fatalf("pick=%d: chose edge %d (opens a 5-chain); want 25 or 54 (1-chain)", pick, got)
		}
		sawSmall = true
	}
	if !sawSmall {
		t.Fatal("no move was made")
	}
}

func TestBestMoveReturnsLegalUndrawnEdge(t *testing.T) {
	b := drawn(2, 7, 40, 41, 55) // an arbitrary partial board
	got, ok := BestMove(b, 3)
	if !ok {
		t.Fatal("ok=false on a board with undrawn edges")
	}
	if !edgeInRange(got) {
		t.Fatalf("edge %d out of range", got)
	}
	if b.Edges[got] != 0 {
		t.Fatalf("edge %d is already drawn (not legal)", got)
	}
}

func TestBestMoveNoMoveOnFullBoard(t *testing.T) {
	if _, ok := BestMove(full(), 0); ok {
		t.Fatal("ok=true on a full board, want false")
	}
}
