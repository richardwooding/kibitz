package hex

import (
	"errors"
	"testing"
)

func place(t *testing.T, b *Board, row, col, side int8) {
	t.Helper()
	if _, err := b.Place(row, col, side); err != nil {
		t.Fatalf("place (%d,%d): %v", row, col, err)
	}
}

// A red vertical line down a single column joins row 0 to row N-1, so red wins.
func TestRedVerticalConnection(t *testing.T) {
	var b Board
	for row := int8(0); row < N; row++ {
		place(t, &b, row, 5, 1)
	}
	w, cells := b.Winner()
	if w != 1 {
		t.Fatalf("winner %d, want 1 (red)", w)
	}
	if len(cells) != N {
		t.Fatalf("win group %d cells, want %d", len(cells), N)
	}
}

// A blue horizontal line across a single row joins col 0 to col N-1, so blue wins.
func TestBlueHorizontalConnection(t *testing.T) {
	var b Board
	for col := int8(0); col < N; col++ {
		place(t, &b, 4, col, 2)
	}
	w, cells := b.Winner()
	if w != 2 {
		t.Fatalf("winner %d, want 2 (blue)", w)
	}
	if len(cells) != N {
		t.Fatalf("win group %d cells, want %d", len(cells), N)
	}
}

// A staircase using the (r-1,c+1) / (r+1,c-1) short-diagonals still connects red
// top-to-bottom — this exercises the hex-specific adjacencies.
func TestRedStaircaseConnection(t *testing.T) {
	var b Board
	// Start at (0,10); each down-left step (+1 row, -1 col) is a neighbour.
	for i := int8(0); i < N; i++ {
		place(t, &b, i, N-1-i, 1)
	}
	if w, _ := b.Winner(); w != 1 {
		t.Fatalf("staircase winner %d, want 1", w)
	}
}

// The two off-diagonal offsets are NOT neighbours: a (r-1,c-1) / (r+1,c+1)
// diagonal does not connect, so it must not win.
func TestNonConnectingDiagonal(t *testing.T) {
	var b Board
	for i := int8(0); i < N; i++ {
		place(t, &b, i, i, 1) // main diagonal (row+col increasing together)
	}
	if w, _ := b.Winner(); w != 0 {
		t.Fatalf("main diagonal should not connect, got winner %d", w)
	}
}

// A scattered handful of stones connects nothing.
func TestNoWinScattered(t *testing.T) {
	var b Board
	place(t, &b, 0, 0, 1)
	place(t, &b, 5, 5, 1)
	place(t, &b, 10, 3, 2)
	place(t, &b, 2, 8, 2)
	if w, cells := b.Winner(); w != 0 || cells != nil {
		t.Fatalf("scatter: winner %d cells %v", w, cells)
	}
}

// A red column missing its last row touches the top but not the bottom edge.
func TestRedAlmostConnection(t *testing.T) {
	var b Board
	for row := int8(0); row < N-1; row++ { // rows 0..N-2, missing the bottom
		place(t, &b, row, 6, 1)
	}
	if w, _ := b.Winner(); w != 0 {
		t.Fatalf("incomplete red column should not win, got %d", w)
	}
}

// Empty board: no winner.
func TestEmptyNoWinner(t *testing.T) {
	var b Board
	if w, cells := b.Winner(); w != 0 || cells != nil {
		t.Fatalf("empty board: winner %d cells %v", w, cells)
	}
}

func TestOffBoardAndOccupied(t *testing.T) {
	var b Board
	place(t, &b, 5, 5, 1)
	if _, err := b.Place(5, 5, 2); !errors.Is(err, ErrOccupied) {
		t.Fatalf("occupied: %v", err)
	}
	if _, err := b.Place(-1, 0, 1); !errors.Is(err, ErrOffBoard) {
		t.Fatalf("off board (row): %v", err)
	}
	if _, err := b.Place(0, N, 1); !errors.Is(err, ErrOffBoard) {
		t.Fatalf("off board (col): %v", err)
	}
}

// The mover's own line wins even when the opponent has many stones elsewhere.
func TestWinAmongOpponentStones(t *testing.T) {
	var b Board
	for row := int8(0); row < N; row++ {
		place(t, &b, row, 0, 1) // red down the left column → top-to-bottom
	}
	// Blue clutters the right side but never connects col 0 to col N-1.
	for row := int8(0); row < N; row++ {
		place(t, &b, row, N-1, 2)
	}
	if w, _ := b.Winner(); w != 1 {
		t.Fatalf("winner %d, want 1", w)
	}
}
