package gomoku

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

func TestHorizontalWin(t *testing.T) {
	var b Board
	for col := int8(3); col < 8; col++ { // five black stones on row 7
		place(t, &b, 7, col, 1)
	}
	w, cells := b.Winner()
	if w != 1 || len(cells) != 5 {
		t.Fatalf("winner %d cells %v", w, cells)
	}
}

func TestVerticalWin(t *testing.T) {
	var b Board
	for row := int8(2); row < 7; row++ {
		place(t, &b, row, 4, 2) // five white stones down column 4
	}
	if w, _ := b.Winner(); w != 2 {
		t.Fatalf("winner %d", w)
	}
}

func TestDiagonalWins(t *testing.T) {
	var b Board
	for i := int8(0); i < 5; i++ { // ↘ diagonal
		place(t, &b, 1+i, 1+i, 1)
	}
	if w, _ := b.Winner(); w != 1 {
		t.Fatalf("falling diagonal: winner %d", w)
	}

	var c Board
	for i := int8(0); i < 5; i++ { // ↗ diagonal
		place(t, &c, 10-i, 2+i, 2)
	}
	if w, _ := c.Winner(); w != 2 {
		t.Fatalf("rising diagonal: winner %d", w)
	}
}

func TestFiveOrMoreWins(t *testing.T) {
	var b Board
	for col := int8(0); col < 6; col++ { // six in a row still wins
		place(t, &b, 0, col, 1)
	}
	if w, cells := b.Winner(); w != 1 || len(cells) != 5 {
		t.Fatalf("overline: winner %d cells %v", w, cells)
	}
}

func TestNoWinScattered(t *testing.T) {
	var b Board
	place(t, &b, 7, 7, 1)
	place(t, &b, 7, 8, 1)
	place(t, &b, 7, 9, 1)
	place(t, &b, 7, 10, 1) // only four in a row
	if w, _ := b.Winner(); w != 0 {
		t.Fatalf("four should not win, got %d", w)
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
	if _, err := b.Place(0, Size, 1); !errors.Is(err, ErrOffBoard) {
		t.Fatalf("off board (col): %v", err)
	}
}
