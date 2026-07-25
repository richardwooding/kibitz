// A capture/liberty-aware 1-ply heuristic bot for the "Hard" seat. This is
// deliberately NOT a strong Go player — no reading, no MCTS — just a scorer
// that is clearly better than random: it takes captures, saves its own groups
// in atari, fights near existing stones, avoids self-atari, and never fills its
// own eyes. All rule checks reuse the engine (applyMove / legalMoves /
// collectGroup) so the bot can only ever return a legal point.
package weiqi

// Heuristic weights for scoring a single candidate placement. Captures dominate
// everything else; the contact and centre terms only break ties between
// otherwise-quiet moves so the bot plays near stones and towards the middle.
const (
	passThreshold    = 0   // best score must exceed this or the bot passes
	captureWeight    = 100 // per opponent stone removed by the move
	saveWeight       = 40  // lifting an own adjacent group out of atari
	selfAtariPenalty = 50  // move leaves its own group on one liberty (no capture)
	ownAdjWeight     = 1   // per adjacent own stone (stay connected)
	oppAdjWeight     = 3   // per adjacent opponent stone (fight, don't scatter)
)

// BestMove returns the heuristic best legal placement for side (1 = black,
// 2 = white) on board b, or pass = true when the board is full, no legal move
// exists, or nothing scores above the threshold (only eye-filling / self-atari
// left). It has no ko context, so it applies no ko constraint (the zero
// forbidden position can never be recreated by a placement); every other
// legality check — empty, non-suicide — comes from the engine's own logic.
func BestMove(b Board, side int8) (row, col int8, pass bool) {
	var forbidden Board // no ko constraint available at this signature
	bestScore := passThreshold
	bestIdx := -1
	for _, p := range legalMoves(b, side, forbidden) {
		idx := int(p)
		if isSinglePointEye(&b, side, idx) {
			continue
		}
		sc, ok := scoreMove(b, side, idx, forbidden)
		if ok && sc > bestScore {
			bestScore = sc
			bestIdx = idx
		}
	}
	if bestIdx < 0 {
		return 0, 0, true
	}
	return int8(bestIdx / N), int8(bestIdx % N), false
}

// scoreMove evaluates placing side's stone at idx (assumed empty and legal).
// It returns ok = false if the engine rejects the placement, otherwise the
// heuristic score. It plays the move on a copy via the engine, so captures,
// liberties, and ko all come from the real rules.
func scoreMove(b Board, side int8, idx int, forbidden Board) (int, bool) {
	nb, captured, err := applyMove(b, side, idx, forbidden)
	if err != nil {
		return 0, false
	}
	caps := len(captured)
	_, libs := collectGroup(&nb, idx)

	score := captureWeight * caps
	if caps == 0 && libs == 1 {
		score -= selfAtariPenalty
	}
	if savesAtari(&b, side, idx, libs) {
		score += saveWeight
	}
	own, opp := adjacencyCounts(&b, side, idx)
	score += ownAdjWeight*own + oppAdjWeight*opp + centerBonus(idx)
	return score, true
}

// savesAtari reports whether playing at idx rescues an own group: some adjacent
// own group currently has exactly one liberty and the resulting group has more.
func savesAtari(b *Board, side int8, idx, newLibs int) bool {
	if newLibs <= 1 {
		return false
	}
	for _, nb := range neighbors(idx) {
		if b[nb] != side {
			continue
		}
		if _, libs := collectGroup(b, nb); libs == 1 {
			return true
		}
	}
	return false
}

// adjacencyCounts counts idx's orthogonal neighbours that are own / opponent
// stones on b (contact / influence).
func adjacencyCounts(b *Board, side int8, idx int) (own, opp int) {
	opp2 := opponent(side)
	for _, nb := range neighbors(idx) {
		switch b[nb] {
		case side:
			own++
		case opp2:
			opp++
		}
	}
	return own, opp
}

// isSinglePointEye reports whether idx is an empty point whose every on-board
// neighbour is a side stone — a single-point eye the bot must never fill.
func isSinglePointEye(b *Board, side int8, idx int) bool {
	if b[idx] != 0 {
		return false
	}
	nbs := neighbors(idx)
	for _, nb := range nbs {
		if b[nb] != side {
			return false
		}
	}
	return len(nbs) > 0
}

// centerBonus rewards central points (0..N/2) using Chebyshev distance to the
// board centre so the bot tends towards the middle when nothing else decides.
func centerBonus(idx int) int {
	dr := abs(idx/N - N/2)
	dc := abs(idx%N - N/2)
	d := dr
	if dc > d {
		d = dc
	}
	return N/2 - d
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
