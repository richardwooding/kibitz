package weiqi

import "testing"

// put places a stone (colour) at (row, col) on b.
func put(b *Board, row, col int, colour int8) { b[row*N+col] = colour }

// TestBestMoveCaptures: a lone white stone in the corner is in atari; black to
// move must play its last liberty to capture it.
func TestBestMoveCaptures(t *testing.T) {
	var b Board
	put(&b, 0, 0, 2) // white stone in atari
	put(&b, 0, 1, 1) // black removes one of its two liberties
	// White (0,0) now has a single liberty at (1,0); black playing there captures.

	row, col, pass := BestMove(b, 1)
	if pass {
		t.Fatal("expected a capturing move, got pass")
	}
	if row != 1 || col != 0 {
		t.Fatalf("expected capture at (1,0), got (%d,%d)", row, col)
	}

	nb, captured, err := applyMove(b, 1, int(row)*N+int(col), Board{})
	if err != nil {
		t.Fatalf("chosen move is illegal: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected to capture 1 stone, captured %d", len(captured))
	}
	if nb[0] != 0 {
		t.Fatalf("captured stone not removed at (0,0): %d", nb[0])
	}
}

// TestBestMoveSkipsEye: black owns a single-point eye at the centre; the bot
// must never choose to fill it (a legal but self-destroying move).
func TestBestMoveSkipsEye(t *testing.T) {
	var b Board
	put(&b, 3, 4, 1)
	put(&b, 5, 4, 1)
	put(&b, 4, 3, 1)
	put(&b, 4, 5, 1)
	// (4,4) is empty with all four neighbours black — a single-point eye.

	row, col, pass := BestMove(b, 1)
	if pass {
		t.Fatal("constructive moves exist; should not pass")
	}
	if row == 4 && col == 4 {
		t.Fatal("bot filled its own single-point eye")
	}
}

// TestBestMoveAvoidsSelfAtari: a corner self-atari is available but a quiet
// connected move is clearly better; the chosen move must not be self-atari.
func TestBestMoveAvoidsSelfAtari(t *testing.T) {
	var b Board
	put(&b, 0, 1, 2) // white; black at corner (0,0) would have a single liberty
	put(&b, 4, 4, 1) // an existing black stone to play beside

	row, col, pass := BestMove(b, 1)
	if pass {
		t.Fatal("constructive moves exist; should not pass")
	}
	if row == 0 && col == 0 {
		t.Fatal("bot chose the corner self-atari")
	}

	nb, captured, err := applyMove(b, 1, int(row)*N+int(col), Board{})
	if err != nil {
		t.Fatalf("chosen move is illegal: %v", err)
	}
	idx := int(row)*N + int(col)
	if _, libs := collectGroup(&nb, idx); libs <= 1 && len(captured) == 0 {
		t.Fatalf("chosen move (%d,%d) is self-atari (libs=%d)", row, col, libs)
	}
}

// TestBestMovePassesOnFullBoard: with no empty point there is no legal move, so
// the bot passes.
func TestBestMovePassesOnFullBoard(t *testing.T) {
	var b Board
	for i := range b {
		b[i] = 1
	}
	if _, _, pass := BestMove(b, 1); !pass {
		t.Fatal("expected pass on a full board")
	}
	if _, _, pass := BestMove(b, 2); !pass {
		t.Fatal("expected pass on a full board for white too")
	}
}
