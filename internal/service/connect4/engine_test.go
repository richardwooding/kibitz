package connect4

import (
	"errors"
	"testing"
)

func drop(t *testing.T, b *Board, col, side int8) {
	t.Helper()
	if _, err := b.Drop(col, side); err != nil {
		t.Fatalf("drop col %d: %v", col, err)
	}
}

func TestVerticalWin(t *testing.T) {
	var b Board
	for i := 0; i < 3; i++ {
		drop(t, &b, 0, 1)
		drop(t, &b, 1, 2)
	}
	drop(t, &b, 0, 1)
	w, cells := b.Winner()
	if w != 1 || len(cells) != 4 {
		t.Fatalf("winner %d cells %v", w, cells)
	}
}

func TestHorizontalWin(t *testing.T) {
	var b Board
	for col := int8(0); col < 3; col++ {
		drop(t, &b, col, 2)
		drop(t, &b, col, 1)
	}
	drop(t, &b, 3, 2)
	w, _ := b.Winner()
	if w != 2 {
		t.Fatalf("winner %d", w)
	}
}

func TestDiagonalWins(t *testing.T) {
	// Rising diagonal for side 1: columns 0..3 with heights 0..3.
	var b Board
	fills := [][2]int8{{1, 2}, {2, 2}, {2, 2}, {3, 2}, {3, 2}, {3, 2}}
	for _, f := range fills {
		drop(t, &b, f[0], f[1])
	}
	for col := int8(0); col < 4; col++ {
		drop(t, &b, col, 1)
	}
	w, _ := b.Winner()
	if w != 1 {
		t.Fatalf("rising diagonal: winner %d", w)
	}

	// Falling diagonal for side 1: columns 0..3 with heights 3..0.
	var c Board
	fills2 := [][2]int8{{0, 2}, {0, 2}, {0, 2}, {1, 2}, {1, 2}, {2, 2}}
	for _, f := range fills2 {
		drop(t, &c, f[0], f[1])
	}
	for col := int8(0); col < 4; col++ {
		drop(t, &c, col, 1)
	}
	w2, _ := c.Winner()
	if w2 != 1 {
		t.Fatalf("falling diagonal: winner %d", w2)
	}
}

func TestColumnFullAndBadColumn(t *testing.T) {
	var b Board
	for i := 0; i < Rows; i++ {
		drop(t, &b, 3, 1)
	}
	if _, err := b.Drop(3, 2); !errors.Is(err, ErrColumnFull) {
		t.Fatalf("full column: %v", err)
	}
	if _, err := b.Drop(7, 1); !errors.Is(err, ErrBadColumn) {
		t.Fatalf("bad column: %v", err)
	}
}

func TestDrawDetection(t *testing.T) {
	// Fill the board with a known drawn pattern: column stripes 112211…
	// per column pairs avoid 4-in-a-row: use pattern by column groups.
	var b Board
	pattern := [Cols]int8{1, 2, 1, 2, 1, 2, 1}
	for col := int8(0); col < Cols; col++ {
		for row := 0; row < Rows; row++ {
			side := pattern[col]
			if (row/2)%2 == 1 {
				side = 3 - side
			}
			drop(t, &b, col, side)
		}
	}
	if w, _ := b.Winner(); w != 0 {
		t.Fatalf("pattern unexpectedly has winner %d", w)
	}
	if !b.Full() {
		t.Fatal("board should be full")
	}
}
