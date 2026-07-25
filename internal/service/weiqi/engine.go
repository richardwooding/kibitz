// Go / Weiqi rules — pure logic, no protocol. Played 9×9 with area (Chinese)
// scoring and komi 6.5 to white. The package is named "weiqi" because "go" is
// a reserved word; the UI shows the display name "Go".
//
// MVP limitation: there is NO dead-stone negotiation. Two consecutive passes
// end the game and the board is scored exactly as it stands, so players must
// capture or fill dead stones before both passing. Area scoring makes filling
// your own territory free, so this is always safe to do.
package weiqi

import "errors"

// N is the board edge; the whole board is N*N points.
const N = 9

// Komi is the compensation added to white's area score. The half-point
// guarantees there is never a draw.
const Komi = 6.5

// Board points: 0 empty, 1 = black stone (P1, moves first), 2 = white (P2).
// Index = row*N + col, row 0 at the top, col 0 at the left.
type Board [N * N]int8

var (
	// ErrOffBoard means the point is outside the board.
	ErrOffBoard = errors.New("weiqi: off the board")
	// ErrOccupied means the point already holds a stone.
	ErrOccupied = errors.New("weiqi: point already occupied")
	// ErrSuicide means the move would leave the placed group with no liberties.
	ErrSuicide = errors.New("weiqi: suicide is illegal")
	// ErrKo means the move would recreate the position before the opponent's
	// previous move (simple positional ko).
	ErrKo = errors.New("weiqi: ko — cannot recreate the previous position")
)

func inBounds(row, col int) bool {
	return row >= 0 && row < N && col >= 0 && col < N
}

// opponent returns the other colour (1↔2).
func opponent(colour int8) int8 { return 3 - colour }

// neighbors returns the orthogonally-adjacent point indices of idx.
func neighbors(idx int) []int {
	row, col := idx/N, idx%N
	out := make([]int, 0, 4)
	if row > 0 {
		out = append(out, idx-N)
	}
	if row < N-1 {
		out = append(out, idx+N)
	}
	if col > 0 {
		out = append(out, idx-1)
	}
	if col < N-1 {
		out = append(out, idx+1)
	}
	return out
}

// collectGroup flood-fills the orthogonally-connected group of stones sharing
// board[start]'s colour and reports the group's point indices plus its liberty
// count (distinct adjacent empty points).
func collectGroup(b *Board, start int) ([]int, int) {
	colour := b[start]
	seen := make([]bool, N*N)
	libSeen := make([]bool, N*N)
	stack := []int{start}
	seen[start] = true
	stones := make([]int, 0, 8)
	liberties := 0
	for len(stack) > 0 {
		idx := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		stones = append(stones, idx)
		for _, nb := range neighbors(idx) {
			switch {
			case b[nb] == 0 && !libSeen[nb]:
				libSeen[nb] = true
				liberties++
			case b[nb] == colour && !seen[nb]:
				seen[nb] = true
				stack = append(stack, nb)
			}
		}
	}
	return stones, liberties
}

// removeCaptured removes every opponent group orthogonally touching idx that
// has no liberties, returning the captured point indices. Because a captured
// group is cleared before the next neighbour is examined, a group touching idx
// from two sides is only removed once.
func removeCaptured(b *Board, opp int8, idx int) []int {
	var captured []int
	for _, nb := range neighbors(idx) {
		if b[nb] != opp {
			continue
		}
		stones, liberties := collectGroup(b, nb)
		if liberties != 0 {
			continue
		}
		for _, s := range stones {
			b[s] = 0
		}
		captured = append(captured, stones...)
	}
	return captured
}

// applyMove places colour's stone at idx on a copy of b, removes any captured
// opponent groups, then rejects suicide and ko. forbidden is the board position
// that may not be recreated (the position before the opponent's previous move);
// the zero Board means "no ko constraint" (it can never be reproduced by a
// placement). Returns the new board and the captured point indices, or an error.
func applyMove(b Board, colour int8, idx int, forbidden Board) (Board, []int, error) {
	if idx < 0 || idx >= N*N {
		return b, nil, ErrOffBoard
	}
	if b[idx] != 0 {
		return b, nil, ErrOccupied
	}
	nb := b
	nb[idx] = colour
	captured := removeCaptured(&nb, opponent(colour), idx)
	if _, liberties := collectGroup(&nb, idx); liberties == 0 {
		return b, nil, ErrSuicide
	}
	if nb == forbidden {
		return b, nil, ErrKo
	}
	return nb, captured, nil
}

// legalMoves returns the empty points where colour may legally place a stone,
// excluding suicide and ko. Used by both the UI and the bot.
func legalMoves(b Board, colour int8, forbidden Board) []int8 {
	var out []int8
	for idx := 0; idx < N*N; idx++ {
		if b[idx] != 0 {
			continue
		}
		if _, _, err := applyMove(b, colour, idx, forbidden); err == nil {
			out = append(out, int8(idx))
		}
	}
	return out
}

// floodEmpty flood-fills the empty region containing start, marking visited
// points in seen, and reports the region size and which colours border it.
func floodEmpty(b *Board, seen []bool, start int) (size int, touchB, touchW bool) {
	stack := []int{start}
	seen[start] = true
	for len(stack) > 0 {
		idx := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		size++
		for _, nb := range neighbors(idx) {
			switch b[nb] {
			case 0:
				if !seen[nb] {
					seen[nb] = true
					stack = append(stack, nb)
				}
			case 1:
				touchB = true
			case 2:
				touchW = true
			}
		}
	}
	return size, touchB, touchW
}

// territory returns the empty points that belong solely to black and solely to
// white. A region bordering both colours (or neither) is neutral (dame).
func territory(b Board) (black, white int) {
	seen := make([]bool, N*N)
	for idx := 0; idx < N*N; idx++ {
		if b[idx] != 0 || seen[idx] {
			continue
		}
		size, touchB, touchW := floodEmpty(&b, seen, idx)
		switch {
		case touchB && !touchW:
			black += size
		case touchW && !touchB:
			white += size
		}
	}
	return black, white
}

// score returns the area score for each side: stones on the board plus
// solely-owned territory. Komi is applied by the caller.
func score(b Board) (black, white int) {
	for _, v := range b {
		switch v {
		case 1:
			black++
		case 2:
			white++
		}
	}
	bTerr, wTerr := territory(b)
	return black + bTerr, white + wTerr
}

// finalScore computes both area scores (white including komi) and the winner
// (colour 1 or 2). The half-point komi rules out ties.
func finalScore(b Board) (blackScore, whiteScore float64, winner int8) {
	ba, wa := score(b)
	blackScore = float64(ba)
	whiteScore = float64(wa) + Komi
	if blackScore > whiteScore {
		return blackScore, whiteScore, 1
	}
	return blackScore, whiteScore, 2
}
