package gomokup

import "testing"

func TestPlaceRejectsOffBoardAndOccupied(t *testing.T) {
	var b Board
	if _, err := b.Place(-1, 0, 1); err != ErrOffBoard {
		t.Fatalf("off board: %v", err)
	}
	if _, err := b.Place(0, Size, 1); err != ErrOffBoard {
		t.Fatalf("off board col: %v", err)
	}
	if _, err := b.Place(5, 5, 1); err != nil {
		t.Fatalf("place: %v", err)
	}
	if _, err := b.Place(5, 5, 2); err != ErrOccupied {
		t.Fatalf("occupied: %v", err)
	}
}

// A run of five of color 3 wins even amid three other colors on the board.
func TestWinnerDetectsAnyColor(t *testing.T) {
	var b Board
	// Scatter other colors that must NOT trigger a win.
	_, _ = b.Place(0, 0, 1)
	_, _ = b.Place(0, 1, 2)
	_, _ = b.Place(1, 0, 4)
	if w, _ := b.Winner(); w != 0 {
		t.Fatalf("no five yet, got winner %d", w)
	}
	// Color 3 makes a horizontal five on row 10, cols 4..8.
	for c := int8(4); c <= 8; c++ {
		if _, err := b.Place(10, c, 3); err != nil {
			t.Fatal(err)
		}
	}
	w, cells := b.Winner()
	if w != 3 {
		t.Fatalf("winner = %d, want 3", w)
	}
	if len(cells) != 5 {
		t.Fatalf("win cells = %v, want 5", cells)
	}
	if cells[0] != int16(10*Size+4) || cells[4] != int16(10*Size+8) {
		t.Fatalf("win cells = %v, want row 10 cols 4..8", cells)
	}
}

func TestWinnerDiagonal(t *testing.T) {
	var b Board
	for i := int8(0); i < 5; i++ {
		if _, err := b.Place(3+i, 3+i, 2); err != nil { // color 2 down-right diagonal
			t.Fatal(err)
		}
	}
	if w, _ := b.Winner(); w != 2 {
		t.Fatalf("diagonal winner = %d, want 2", w)
	}
}

func TestFull(t *testing.T) {
	var b Board
	if b.Full() {
		t.Fatal("empty board reported full")
	}
	for i := range b {
		b[i] = 1
	}
	if !b.Full() {
		t.Fatal("filled board not reported full")
	}
}
