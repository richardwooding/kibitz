package dots

// The solo "Play the computer" bot's Hard move for Dots and Boxes. Unlike the
// alpha-beta game bots, this is a pure heuristic over the engine's own Board:
// it never searches a tree, so it stays cheap on the 5×5 grid while playing far
// better than a random legal move.
//
// The classic three-tier Dots heuristic:
//
//  1. Take every free box. Completing a box grants another turn, so grabbing a
//     box that is one edge short is always correct at this tier.
//  2. Otherwise play a SAFE edge — one that does not bring any box to three
//     sides, i.e. does not hand the opponent a free box on their next turn.
//  3. If no safe edge exists (every move opens something), give away the
//     LEAST: play the edge that opens the smallest chain of boxes.
//
// The chain estimate (chainSize) is deliberately simple: it assumes the
// opponent greedily grabs the whole chain the sacrifice opens. The full
// double-cross endgame (declining the last two boxes of a chain to keep
// control) is OUT OF SCOPE — this bot always takes and always opens the
// smallest chain, which is a strong-but-not-perfect endgame.

// BestMove returns the Hard bot's chosen edge for board b. It is a pure
// function: all randomness is injected via pick (any int — e.g. rand.Int() or a
// board hash), which selects among equally-good candidate edges so the bot does
// not lock into one deterministic line. ok is false only when no undrawn edge
// remains. Only b.Edges is consulted; b.Owner is ignored (box ownership does not
// affect which edge is best to draw).
func BestMove(b Board, pick int) (edge int8, ok bool) {
	legal := b.Legal()
	if len(legal) == 0 {
		return 0, false
	}
	if free := freeBoxEdges(&b, legal); len(free) > 0 {
		return pickFrom(free, pick), true // 1. take a free box
	}
	if safe := safeEdges(&b, legal); len(safe) > 0 {
		return pickFrom(safe, pick), true // 2. don't hand over a box
	}
	return leastDamage(&b, legal, pick), true // 3. open the smallest chain
}

// freeBoxEdges lists undrawn edges that immediately complete at least one box.
func freeBoxEdges(b *Board, legal []int8) []int8 {
	var out []int8
	for _, e := range legal {
		if len(b.Completes(e)) > 0 {
			out = append(out, e)
		}
	}
	return out
}

// safeEdges lists undrawn edges that do NOT bring any box to three sides.
func safeEdges(b *Board, legal []int8) []int8 {
	var out []int8
	for _, e := range legal {
		if !createsThirdSide(b, e) {
			out = append(out, e)
		}
	}
	return out
}

// leastDamage picks, among all edges, one that opens the smallest chain — used
// only when every remaining edge creates a third side (no safe move).
func leastDamage(b *Board, legal []int8, pick int) int8 {
	min := chainSize(*b, legal[0])
	for _, e := range legal[1:] {
		if c := chainSize(*b, e); c < min {
			min = c
		}
	}
	var tied []int8
	for _, e := range legal {
		if chainSize(*b, e) == min {
			tied = append(tied, e)
		}
	}
	return pickFrom(tied, pick)
}

// createsThirdSide reports whether drawing edge e brings an adjacent box to
// exactly three drawn sides — i.e. gifts the opponent a free box next turn.
func createsThirdSide(b *Board, e int8) bool {
	for _, box := range adjacentBoxes(e) {
		if d, _ := drawnSides(b, box); d == 2 {
			return true
		}
	}
	return false
}

// chainSize estimates how many boxes drawing e gives away: it draws e on a copy
// of the board, then greedily claims every box that reaches three sides,
// following the cascade, and returns the number of boxes claimed. b is taken by
// value so the caller's board is untouched.
func chainSize(b Board, e int8) int {
	b.Edges[e] = 1
	count := 0
	for {
		next, found := threeSidedEdge(&b)
		if !found {
			return count
		}
		b.Edges[next] = 1
		count++
	}
}

// threeSidedEdge finds a box with exactly three drawn sides and returns its one
// undrawn edge (the edge that completes it), or ok=false if none exists.
func threeSidedEdge(b *Board) (edge int8, ok bool) {
	for box := int8(0); int(box) < NumBoxes; box++ {
		if d, u := drawnSides(b, box); d == 3 && u >= 0 {
			return u, true
		}
	}
	return 0, false
}

// drawnSides returns how many of box's four edges are drawn and, when exactly
// one is undrawn, that undrawn edge id (else -1).
func drawnSides(b *Board, box int8) (drawn int, undrawn int8) {
	undrawn = -1
	for _, e := range boxEdges(box) {
		if b.Edges[e] != 0 {
			drawn++
		} else {
			undrawn = e
		}
	}
	return drawn, undrawn
}

// pickFrom returns edges[pick mod len], varying the choice among tied
// candidates without ever going out of range (pick may be any int).
func pickFrom(edges []int8, pick int) int8 {
	n := len(edges)
	idx := pick % n
	if idx < 0 {
		idx += n
	}
	return edges[idx]
}
