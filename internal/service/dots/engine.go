// Dots and Boxes rules — pure logic, no protocol.
//
// The grid is 5×5 boxes, so 6×6 dots. Edges are numbered row-major in two
// contiguous blocks:
//
//	Horizontal edges: ids 0..29 — 6 rows (r = 0..5) of 5 (c = 0..4).
//	  id = r*5 + c ; the edge joins dot (r,c) to dot (r,c+1).
//	Vertical edges:   ids 30..59 — 5 rows (r = 0..4) of 6 (c = 0..5).
//	  id = 30 + r*6 + c ; the edge joins dot (r,c) to dot (r+1,c).
//
// A box is identified by (br,bc) with br,bc = 0..4, indexed br*5 + bc (0..24).
// Its four edges are:
//
//	top    = br*5 + bc          (horizontal, row br)
//	bottom = (br+1)*5 + bc      (horizontal, row br+1)
//	left   = 30 + br*6 + bc     (vertical,  col bc)
//	right  = 30 + br*6 + bc + 1 (vertical,  col bc+1)
package dots

import (
	"errors"
	"fmt"
)

const (
	Boxes    = 5               // boxes per side (5×5 grid)
	Dots     = Boxes + 1       // dots per side (6×6)
	HEdges   = Dots * Boxes    // horizontal edges: 6 rows × 5 = 30
	VEdges   = Boxes * Dots    // vertical edges:   5 rows × 6 = 30
	NumEdges = HEdges + VEdges // 60 total edges
	NumBoxes = Boxes * Boxes   // 25 boxes
)

// Board tracks which edges are drawn and who owns each box.
// Edges: 0 undrawn, 1 drawn. Owner: 0 none, 1 = P1, 2 = P2.
type Board struct {
	Edges [NumEdges]int8 `cbor:"1,keyasint"`
	Owner [NumBoxes]int8 `cbor:"2,keyasint"`
}

var (
	ErrOffBoard = errors.New("dots: edge id out of range")
	ErrDrawn    = errors.New("dots: edge already drawn")
)

func edgeInRange(e int8) bool { return e >= 0 && int(e) < NumEdges }

// boxEdges returns the four edge ids bordering a box (top, bottom, left, right).
func boxEdges(box int8) [4]int8 {
	br := int(box) / Boxes
	bc := int(box) % Boxes
	top := int8(br*Boxes + bc)
	bottom := int8((br+1)*Boxes + bc)
	left := int8(HEdges + br*Dots + bc)
	right := int8(HEdges + br*Dots + bc + 1)
	return [4]int8{top, bottom, left, right}
}

// adjacentBoxes returns the 1 or 2 box ids that border edge e.
func adjacentBoxes(e int8) []int8 {
	if int(e) < HEdges { // horizontal edge
		r, c := int(e)/Boxes, int(e)%Boxes
		var out []int8
		if r > 0 { // box above
			out = append(out, int8((r-1)*Boxes+c))
		}
		if r < Boxes { // box below
			out = append(out, int8(r*Boxes+c))
		}
		return out
	}
	vv := int(e) - HEdges // vertical edge
	r, c := vv/Dots, vv%Dots
	var out []int8
	if c > 0 { // box to the left
		out = append(out, int8(r*Boxes+c-1))
	}
	if c < Boxes { // box to the right
		out = append(out, int8(r*Boxes+c))
	}
	return out
}

// boxComplete reports whether all four edges of a box are drawn.
func (b *Board) boxComplete(box int8) bool {
	for _, e := range boxEdges(box) {
		if b.Edges[e] == 0 {
			return false
		}
	}
	return true
}

// completesBox reports whether drawing edge e would complete box (its other
// three edges are already drawn). Non-mutating.
func (b *Board) completesBox(box, e int8) bool {
	drawn, has := 0, false
	for _, be := range boxEdges(box) {
		if be == e {
			has = true
			continue
		}
		if b.Edges[be] != 0 {
			drawn++
		}
	}
	return has && drawn == 3
}

// Completes lists the boxes that drawing edge e would complete right now
// (before e is drawn). Used by the UI and bot; length 0, 1 or 2.
func (b *Board) Completes(e int8) []int8 {
	var out []int8
	for _, box := range adjacentBoxes(e) {
		if b.completesBox(box, e) {
			out = append(out, box)
		}
	}
	return out
}

// Apply draws edge e for side (1 or 2), claiming any boxes it completes, and
// returns how many boxes were claimed (0, 1 or 2). An out-of-range or already
// drawn edge is rejected.
func (b *Board) Apply(e, side int8) (int, error) {
	if !edgeInRange(e) {
		return 0, ErrOffBoard
	}
	if b.Edges[e] != 0 {
		return 0, ErrDrawn
	}
	b.Edges[e] = 1
	claimed := 0
	for _, box := range adjacentBoxes(e) {
		if b.Owner[box] == 0 && b.boxComplete(box) {
			b.Owner[box] = side
			claimed++
		}
	}
	return claimed, nil
}

// Legal returns the ids of all undrawn edges.
func (b *Board) Legal() []int8 {
	out := make([]int8, 0, NumEdges)
	for e := int8(0); int(e) < NumEdges; e++ {
		if b.Edges[e] == 0 {
			out = append(out, e)
		}
	}
	return out
}

// Full reports whether every edge is drawn (the game-over condition).
func (b *Board) Full() bool {
	for _, e := range b.Edges {
		if e == 0 {
			return false
		}
	}
	return true
}

// Score returns the box counts for P1 and P2.
func (b *Board) Score() (p1, p2 int) {
	for _, o := range b.Owner {
		switch o {
		case 1:
			p1++
		case 2:
			p2++
		}
	}
	return p1, p2
}

// EdgeLabel returns a readable coordinate for edge e: an orientation glyph
// ("—" horizontal / "|" vertical) plus a column letter and 1-based row of the
// edge's origin dot, e.g. "—c2" or "|d3".
func EdgeLabel(e int8) string {
	if int(e) < HEdges {
		r, c := int(e)/Boxes, int(e)%Boxes
		return fmt.Sprintf("—%c%d", 'a'+c, r+1)
	}
	vv := int(e) - HEdges
	r, c := vv/Dots, vv%Dots
	return fmt.Sprintf("|%c%d", 'a'+c, r+1)
}
