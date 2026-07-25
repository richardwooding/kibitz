// Connect Four rules — pure logic, no protocol.
package connect4

import "errors"

const (
	Cols = 7
	Rows = 6
)

// Board cells: 0 empty, 1 = P1 disc, 2 = P2 disc. Index = col*Rows + row,
// row 0 at the bottom.
type Board [Cols * Rows]int8

var (
	ErrColumnFull = errors.New("connect4: column is full")
	ErrBadColumn  = errors.New("connect4: no such column")
)

// Drop places a disc for side (1 or 2) in col, returning the landing row.
func (b *Board) Drop(col int8, side int8) (int8, error) {
	if col < 0 || col >= Cols {
		return 0, ErrBadColumn
	}
	for row := int8(0); row < Rows; row++ {
		if b[col*Rows+row] == 0 {
			b[col*Rows+row] = side
			return row, nil
		}
	}
	return 0, ErrColumnFull
}

// Full reports a drawn (completely filled) board.
func (b *Board) Full() bool {
	for col := int8(0); col < Cols; col++ {
		if b[col*Rows+Rows-1] == 0 {
			return false
		}
	}
	return true
}

// run returns the four cell indices of a same-side line starting at (col,row)
// in direction (dcol,drow), or nil if there aren't four in a row from there.
func (b *Board) run(col, row, dcol, drow, side int8) []int8 {
	cells := []int8{col*Rows + row}
	c, r := col, row
	for i := 0; i < 3; i++ {
		c += dcol
		r += drow
		if c < 0 || c >= Cols || r < 0 || r >= Rows || b[c*Rows+r] != side {
			return nil
		}
		cells = append(cells, c*Rows+r)
	}
	return cells
}

// Winner returns the winning side (1/2) and its four cell indices, or 0.
func (b *Board) Winner() (int8, []int8) {
	dirs := [4][2]int8{{1, 0}, {0, 1}, {1, 1}, {1, -1}} // (dcol, drow)
	for col := int8(0); col < Cols; col++ {
		for row := int8(0); row < Rows; row++ {
			side := b[col*Rows+row]
			if side == 0 {
				continue
			}
			for _, d := range dirs {
				if cells := b.run(col, row, d[0], d[1], side); cells != nil {
					return side, cells
				}
			}
		}
	}
	return 0, nil
}
