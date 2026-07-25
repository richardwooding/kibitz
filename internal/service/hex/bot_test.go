package hex

import "testing"

// A red column with a single one-cell gap: the only move that connects top to
// bottom is filling that gap, so the bot must play it.
func TestBestMovePlaysWinningConnection(t *testing.T) {
	var b Board
	const col = 2
	for row := int8(0); row < N; row++ {
		if row == 5 {
			continue // the gap
		}
		place(t, &b, row, col, 1)
	}
	row, c, ok := BestMove(b, 1)
	if !ok {
		t.Fatalf("ok=false, want a move")
	}
	if row != 5 || c != col {
		t.Fatalf("BestMove = (%d,%d), want the completing cell (5,%d)", row, c, col)
	}
}

// A red column split by a three-cell gap has no single winning move. The bot
// should extend/bridge near the stones (the centre gap cell wins the score tie)
// rather than play a far-away corner.
func TestBestMovePrefersConnectingCell(t *testing.T) {
	var b Board
	const col = 5
	for _, row := range []int8{0, 1, 2, 3, 7, 8, 9, 10} {
		place(t, &b, row, col, 1)
	}
	row, c, ok := BestMove(b, 1)
	if !ok {
		t.Fatalf("ok=false, want a move")
	}
	// The centre (5,5) bridges the gap and is nearest the centre on ties.
	if row != 5 || c != col {
		t.Fatalf("BestMove = (%d,%d), want the bridging centre cell (5,%d)", row, c, col)
	}
	// It must not be a far, unconnected corner.
	if (row == 0 && c == 0) || (row == N-1 && c == N-1) {
		t.Fatalf("BestMove picked a far corner (%d,%d)", row, c)
	}
}

// The chosen move must always be a legal (empty, in-bounds) cell.
func TestBestMoveReturnsLegalCell(t *testing.T) {
	var b Board
	place(t, &b, 5, 5, 1)
	place(t, &b, 4, 6, 2)
	place(t, &b, 6, 4, 1)
	row, c, ok := BestMove(b, 2)
	if !ok {
		t.Fatalf("ok=false, want a move")
	}
	if !inBounds(int(row), int(c)) {
		t.Fatalf("BestMove = (%d,%d) out of bounds", row, c)
	}
	if b[int(row)*N+int(c)] != 0 {
		t.Fatalf("BestMove = (%d,%d) is not empty", row, c)
	}
}

// A full board has no legal move.
func TestBestMoveFullBoard(t *testing.T) {
	var b Board
	for i := range b {
		b[i] = 1
	}
	if _, _, ok := BestMove(b, 1); ok {
		t.Fatalf("ok=true on a full board, want false")
	}
}
