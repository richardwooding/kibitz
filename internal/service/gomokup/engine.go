// Package gomokup is "Gomoku Party": five-in-a-row for 2–4 players on one shared
// board with rotating turns. The rules engine is pure logic (no protocol): a
// stone carries its seat's color (1..N), and a run of five of any one color
// wins — a near-verbatim generalization of the two-player gomoku engine.
package gomokup

import "errors"

// Size is the board edge. A 19×19 board (Go-sized) gives room for up to four
// players' stones.
const Size = 19

// Board cells: 0 empty, 1..N = a seat's stone color (seat index + 1).
// Index = row*Size + col, row 0 at the top, col 0 at the left.
type Board [Size * Size]int8

var (
	ErrOffBoard = errors.New("gomokup: off the board")
	ErrOccupied = errors.New("gomokup: cell already taken")
)

func inBounds(row, col int) bool {
	return row >= 0 && row < Size && col >= 0 && col < Size
}

// Place puts a stone of the given color (1..N) at (row, col), returning its
// board index.
func (b *Board) Place(row, col, color int8) (int16, error) {
	if !inBounds(int(row), int(col)) {
		return 0, ErrOffBoard
	}
	idx := int(row)*Size + int(col)
	if b[idx] != 0 {
		return 0, ErrOccupied
	}
	b[idx] = color
	return int16(idx), nil
}

// Full reports a completely filled board (a draw if no one has five).
func (b *Board) Full() bool {
	for _, v := range b {
		if v == 0 {
			return false
		}
	}
	return true
}

// run returns the five cell indices of a same-color line starting at (row,col)
// in direction (drow,dcol), or nil if there aren't five from there.
func (b *Board) run(row, col, drow, dcol int, color int8) []int16 {
	cells := []int16{int16(row*Size + col)}
	r, c := row, col
	for i := 0; i < 4; i++ {
		r += drow
		c += dcol
		if !inBounds(r, c) || b[r*Size+c] != color {
			return nil
		}
		cells = append(cells, int16(r*Size+c))
	}
	return cells
}

// Winner returns the winning color (1..N) and its five cell indices, or 0.
// Five or more in a row wins; the first five of the line are returned.
func (b *Board) Winner() (int8, []int16) {
	dirs := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}} // (drow, dcol)
	for row := 0; row < Size; row++ {
		for col := 0; col < Size; col++ {
			color := b[row*Size+col]
			if color == 0 {
				continue
			}
			for _, d := range dirs {
				if cells := b.run(row, col, d[0], d[1], color); cells != nil {
					return color, cells
				}
			}
		}
	}
	return 0, nil
}
