// Gomoku (five-in-a-row) rules — pure logic, no protocol.
package gomoku

import "errors"

// Size is the board edge: the standard 15×15 Gomoku/Gobang board.
const Size = 15

// Board cells: 0 empty, 1 = black stone (P1, moves first), 2 = white (P2).
// Index = row*Size + col, row 0 at the top, col 0 at the left.
type Board [Size * Size]int8

var (
	ErrOffBoard = errors.New("gomoku: off the board")
	ErrOccupied = errors.New("gomoku: cell already taken")
)

func inBounds(row, col int) bool {
	return row >= 0 && row < Size && col >= 0 && col < Size
}

// Place puts a stone for side (1 or 2) at (row, col), returning its board index.
func (b *Board) Place(row, col, side int8) (int16, error) {
	if !inBounds(int(row), int(col)) {
		return 0, ErrOffBoard
	}
	idx := int(row)*Size + int(col)
	if b[idx] != 0 {
		return 0, ErrOccupied
	}
	b[idx] = side
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

// run returns the five cell indices of a same-side line starting at (row,col)
// in direction (drow,dcol), or nil if there aren't five in a row from there.
func (b *Board) run(row, col, drow, dcol int, side int8) []int16 {
	cells := []int16{int16(row*Size + col)}
	r, c := row, col
	for i := 0; i < 4; i++ {
		r += drow
		c += dcol
		if !inBounds(r, c) || b[r*Size+c] != side {
			return nil
		}
		cells = append(cells, int16(r*Size+c))
	}
	return cells
}

// Winner returns the winning side (1/2) and its five cell indices, or 0.
// Five *or more* in a row wins; the first five of the line are returned.
func (b *Board) Winner() (int8, []int16) {
	dirs := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}} // (drow, dcol)
	for row := 0; row < Size; row++ {
		for col := 0; col < Size; col++ {
			side := b[row*Size+col]
			if side == 0 {
				continue
			}
			for _, d := range dirs {
				if cells := b.run(row, col, d[0], d[1], side); cells != nil {
					return side, cells
				}
			}
		}
	}
	return 0, nil
}
