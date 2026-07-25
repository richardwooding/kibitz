// A "Hard" Xiangqi bot: alpha-beta negamax over the engine's own legal-move
// generation, apply, and check/terminal helpers (engine.go). Everything here is
// a pure function of the board — no service state, no protocol. The bot reuses
// LegalMoves / Apply / Winner / InCheck rather than reimplementing any rules.
package xiangqi

import "sort"

const (
	// botDepth is the search depth in plies. Depth 3 from the opening searches
	// well under ~300ms (leaves only evaluate; move generation is skipped there).
	botDepth = 3
	// mateScore is the magnitude of a checkmate; adjusted by ply so the search
	// prefers faster mates and slower losses. Kept below botInfinity.
	mateScore = 1_000_000
	// botInfinity bounds the alpha-beta window.
	botInfinity = 1 << 30
	// generalValue is the General's material sentinel (king safety); large
	// relative to the other pieces but well below mateScore.
	generalValue = 6000
)

// BestMove returns the best move for side (Red = +1, Black = -1, matching the
// engine's convention) found by alpha-beta negamax. ok is false when side has
// no legal move (already mated or stalemated). The returned move is always a
// member of LegalMoves(b, side), so it is never illegal or self-checking.
func BestMove(b Board, side int8) (from, to int8, ok bool) {
	moves := orderedMoves(b, side)
	if len(moves) == 0 {
		return 0, 0, false
	}
	best := -botInfinity
	alpha := -botInfinity
	for _, m := range moves {
		score := -negamax(Apply(b, m[0], m[1]), -side, botDepth-1, 1, -botInfinity, -alpha)
		if score > best {
			best = score
			from, to = m[0], m[1]
		}
		if best > alpha {
			alpha = best
		}
	}
	return from, to, true
}

// negamax returns the value of the position for side to move, searching depth
// more plies. ply is the distance from the root, used to scale mate scores.
func negamax(b Board, side int8, depth, ply, alpha, beta int) int {
	if depth == 0 {
		return evaluate(b, side)
	}
	moves := orderedMoves(b, side)
	if len(moves) == 0 {
		// Side to move has no legal reply: checkmate or stalemate both lose in
		// Xiangqi. Nearer losses (smaller ply) score more negatively.
		return -mateScore + ply
	}
	best := -botInfinity
	for _, m := range moves {
		score := -negamax(Apply(b, m[0], m[1]), -side, depth-1, ply+1, -beta, -alpha)
		if score > best {
			best = score
		}
		if best > alpha {
			alpha = best
		}
		if alpha >= beta {
			break
		}
	}
	return best
}

// orderedMoves returns the legal moves for side with captures first (most
// valuable victim first) to sharpen alpha-beta pruning.
func orderedMoves(b Board, side int8) [][2]int8 {
	moves := LegalMoves(b, side)
	sort.SliceStable(moves, func(i, j int) bool {
		return captureGain(b, moves[i]) > captureGain(b, moves[j])
	})
	return moves
}

// captureGain is the material value of the piece captured by move m, or 0 for a
// quiet move.
func captureGain(b Board, m [2]int8) int {
	victim := b[m[1]]
	if victim == 0 {
		return 0
	}
	return baseValue(abs8(victim), m[1], sign(victim))
}

// evaluate scores the position from side's perspective: own pieces add, enemy
// pieces subtract (material plus a light positional term).
func evaluate(b Board, side int8) int {
	score := 0
	for i := int8(0); i < 90; i++ {
		p := b[i]
		if p == 0 {
			continue
		}
		v := pieceScore(abs8(p), i, sign(p))
		if sign(p) == side {
			score += v
		} else {
			score -= v
		}
	}
	return score
}

// pieceScore is the total value of a piece of type t on square idx belonging to
// pieceSide: material plus a light positional bonus.
func pieceScore(t, idx, pieceSide int8) int {
	return baseValue(t, idx, pieceSide) + positionBonus(idx, pieceSide)
}

// baseValue is the material value of piece type t (Soldiers rise past the
// river). idx and pieceSide only matter for Soldiers.
func baseValue(t, idx, pieceSide int8) int {
	switch t {
	case General:
		return generalValue
	case Chariot:
		return 900
	case Cannon:
		return 450
	case Horse:
		return 400
	case Advisor:
		return 200
	case Elephant:
		return 200
	case Soldier:
		return soldierValue(idx, pieceSide)
	}
	return 0
}

// soldierValue is 100 before the river, rising toward ~200 as the soldier
// advances into enemy territory.
func soldierValue(idx, pieceSide int8) int {
	r := rankOf(idx)
	crossed := (pieceSide == Red && r >= 5) || (pieceSide == Black && r <= 4)
	if !crossed {
		return 100
	}
	adv := r - 4
	if pieceSide == Black {
		adv = 5 - r
	}
	return 100 + int(adv)*20
}

// positionBonus is a light term rewarding central files and advancement toward
// the enemy; always small relative to material.
func positionBonus(idx, pieceSide int8) int {
	return centreFileBonus(fileOf(idx)) + advanceBonus(rankOf(idx), pieceSide)
}

// centreFileBonus rewards occupying central files.
func centreFileBonus(f int8) int {
	switch f {
	case 4:
		return 8
	case 3, 5:
		return 5
	case 2, 6:
		return 2
	}
	return 0
}

// advanceBonus lightly rewards ranks nearer the enemy edge (Red advances up,
// Black down).
func advanceBonus(r, pieceSide int8) int {
	if pieceSide == Red {
		return int(r)
	}
	return int(9 - r)
}
