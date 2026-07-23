// Reversi (Othello) rules — pure logic, no protocol.
//
// Sides are +1 (Black, P1, moves first) and -1 (White, P2). Passes are a
// pure function of the position, so they are COMPUTED by Advance and never
// sent over the wire — a pass message would only be a desync surface.
package reversi

import "errors"

// Board cells: 0 empty, +1 black, -1 white. Index = row*8 + col.
type Board [64]int8

var ErrIllegalPlace = errors.New("reversi: illegal placement")

// Start is the standard central position.
func Start() Board {
	var b Board
	b[27], b[36] = -1, -1 // white d4, e5
	b[28], b[35] = 1, 1   // black e4, d5
	return b
}

var dirs = [8][2]int8{
	{-1, -1}, {-1, 0}, {-1, 1},
	{0, -1}, {0, 1},
	{1, -1}, {1, 0}, {1, 1},
}

// flipsInDir returns the squares flipped by placing side at (r,c) toward d,
// or nil when the line doesn't close.
func flipsInDir(b *Board, side, r, c int8, d [2]int8) []int8 {
	var line []int8
	rr, cc := r+d[0], c+d[1]
	for rr >= 0 && rr < 8 && cc >= 0 && cc < 8 {
		sq := rr*8 + cc
		switch b[sq] {
		case -side:
			line = append(line, sq)
		case side:
			if len(line) > 0 {
				return line
			}
			return nil
		default:
			return nil
		}
		rr += d[0]
		cc += d[1]
	}
	return nil
}

// LegalSquares lists every square where side may place.
func LegalSquares(b Board, side int8) []int8 {
	var out []int8
	for sq := int8(0); sq >= 0 && sq < 64; sq++ {
		if b[sq] != 0 {
			continue
		}
		r, c := sq/8, sq%8
		for _, d := range dirs {
			if flipsInDir(&b, side, r, c, d) != nil {
				out = append(out, sq)
				break
			}
		}
	}
	return out
}

// Place validates and applies a placement, flipping all closed lines.
func Place(b Board, side, sq int8) (Board, error) {
	if sq < 0 || sq >= 64 || b[sq] != 0 {
		return b, ErrIllegalPlace
	}
	r, c := sq/8, sq%8
	flipped := false
	for _, d := range dirs {
		for _, f := range flipsInDir(&b, side, r, c, d) {
			b[f] = side
			flipped = true
		}
	}
	if !flipped {
		return b, ErrIllegalPlace
	}
	b[sq] = side
	return b, nil
}

// Advance decides who moves after side just moved: the opponent if it has a
// move; otherwise the mover again (a computed pass); otherwise the game is
// over (covers two-pass and full-board endings identically).
func Advance(b Board, justMoved int8) (next int8, passed, over bool) {
	opp := -justMoved
	if len(LegalSquares(b, opp)) > 0 {
		return opp, false, false
	}
	if len(LegalSquares(b, justMoved)) > 0 {
		return justMoved, true, false
	}
	return 0, false, true
}

// Counts returns the disc totals.
func Counts(b Board) (black, white int) {
	for _, v := range b {
		switch v {
		case 1:
			black++
		case -1:
			white++
		}
	}
	return
}
