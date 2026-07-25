package hex

// Hard bot for Hex: the classic shortest-connection-path evaluation. It is a
// pure function over the engine's Board/side convention (side 1 = red, connects
// top↔bottom; side 2 = blue, connects left↔right) and reuses the engine's
// Winner/adjacency/edge helpers, so it stays in lock-step with the rules.
//
// Seat→side mapping (see service.go): the engine side is int8(game.Side)+1, so
// seat P1 → side 1 (red) and seat P2 → side 2 (blue).
//
// Heuristic:
//   - If any empty cell immediately completes side's edge-to-edge connection,
//     play it (verified with the engine's Winner check).
//   - Otherwise each candidate scores as (opponent connect distance) − (own
//     connect distance) after tentatively placing side there; the max wins,
//     ties broken toward the centre. This rewards both extending your own
//     connection and blocking the opponent's.
//
// "Connect distance" for a colour is the shortest path (0-1 BFS) from its start
// edge to its goal edge over the six-neighbour rhombus graph, treating that
// colour's stones as cost 0, empty cells as cost 1, and the opponent's stones
// as impassable — i.e. the minimum number of additional stones needed to
// connect. A virtual source sits off-board adjacent to the whole start edge.
//
// To bound cost, candidates are restricted to empty cells within radius 2 of
// any stone (plus the board centre on an empty board).

const infDist = 1 << 30

// BestMove returns the Hard bot's chosen placement for side (1 = red, 2 = blue).
// ok is false only when the board is full.
func BestMove(b Board, side int8) (row, col int8, ok bool) {
	empties := emptyCells(&b)
	if len(empties) == 0 {
		return 0, 0, false
	}
	if idx, won := winningMove(&b, side, empties); won {
		return int8(idx / N), int8(idx % N), true
	}
	best := pickBest(&b, side, candidateCells(&b, empties))
	return int8(best / N), int8(best % N), true
}

// winningMove returns the first empty cell that immediately connects side's two
// edges, using the engine's Winner check.
func winningMove(b *Board, side int8, empties []int) (int, bool) {
	for _, idx := range empties {
		b[idx] = side
		w, _ := b.Winner()
		b[idx] = 0
		if w == side {
			return idx, true
		}
	}
	return 0, false
}

// pickBest scores each candidate and returns the highest, ties broken toward
// the centre.
func pickBest(b *Board, side int8, cands []int) int {
	opp := opponent(side)
	bestIdx, bestScore, bestCentre := cands[0], -infDist, infDist
	for _, idx := range cands {
		s := scoreMove(b, side, opp, idx)
		c := centreDist(idx)
		if s > bestScore || (s == bestScore && c < bestCentre) {
			bestIdx, bestScore, bestCentre = idx, s, c
		}
	}
	return bestIdx
}

// scoreMove tentatively plays side at idx and returns opponent connect distance
// minus own connect distance.
func scoreMove(b *Board, side, opp int8, idx int) int {
	b[idx] = side
	own := connectDistance(b, side)
	other := connectDistance(b, opp)
	b[idx] = 0
	return other - own
}

// connectDistance is the minimum number of empty cells side must still fill to
// join its two edges (0 means already connected, infDist means impossible),
// computed with a 0-1 BFS over the six-neighbour graph.
func connectDistance(b *Board, side int8) int {
	dist := make([]int, N*N)
	done := make([]bool, N*N)
	for i := range dist {
		dist[i] = infDist
	}
	dq := &deque{}
	seedStart(b, side, dist, dq)
	best := infDist
	for !dq.empty() {
		idx := dq.popFront()
		if done[idx] {
			continue
		}
		done[idx] = true
		if atGoalEdge(side, idx) && dist[idx] < best {
			best = dist[idx]
		}
		relax(b, side, idx, dist, dq)
	}
	return best
}

// seedStart primes the BFS from every non-blocked cell on side's start edge
// (row 0 for red, col 0 for blue).
func seedStart(b *Board, side int8, dist []int, dq *deque) {
	for i := 0; i < N; i++ {
		idx := i
		if side == 2 {
			idx = i * N
		}
		w := cellCost(b, idx, side)
		if w < 0 {
			continue
		}
		if w < dist[idx] {
			dist[idx] = w
			dq.pushCost(idx, w)
		}
	}
}

// relax explores idx's six neighbours, relaxing their distances.
func relax(b *Board, side int8, idx int, dist []int, dq *deque) {
	row, col := idx/N, idx%N
	for _, off := range neighbourOffsets {
		r, c := row+off[0], col+off[1]
		if !inBounds(r, c) {
			continue
		}
		n := r*N + c
		w := cellCost(b, n, side)
		if w < 0 {
			continue
		}
		if nd := dist[idx] + w; nd < dist[n] {
			dist[n] = nd
			dq.pushCost(n, w)
		}
	}
}

// cellCost is 0 for side's own stone, 1 for an empty cell, and -1 (impassable)
// for the opponent's stone.
func cellCost(b *Board, idx int, side int8) int {
	switch b[idx] {
	case 0:
		return 1
	case side:
		return 0
	default:
		return -1
	}
}

// candidateCells restricts scoring to empty cells within radius 2 of any stone
// (the only cells that can shift a connection), or just the centre on an empty
// board. It falls back to all empties if nothing qualifies.
func candidateCells(b *Board, empties []int) []int {
	if boardEmpty(b) {
		return []int{centreIdx()}
	}
	cands := make([]int, 0, len(empties))
	for _, idx := range empties {
		if nearStone(b, idx) {
			cands = append(cands, idx)
		}
	}
	if len(cands) == 0 {
		return empties
	}
	return cands
}

// nearStone reports whether any stone sits within radius 2 (row/col) of idx.
func nearStone(b *Board, idx int) bool {
	row, col := idx/N, idx%N
	for dr := -2; dr <= 2; dr++ {
		for dc := -2; dc <= 2; dc++ {
			r, c := row+dr, col+dc
			if inBounds(r, c) && b[r*N+c] != 0 {
				return true
			}
		}
	}
	return false
}

func boardEmpty(b *Board) bool {
	for i := 0; i < N*N; i++ {
		if b[i] != 0 {
			return false
		}
	}
	return true
}

func emptyCells(b *Board) []int {
	out := make([]int, 0, N*N)
	for i := 0; i < N*N; i++ {
		if b[i] == 0 {
			out = append(out, i)
		}
	}
	return out
}

func opponent(side int8) int8 {
	if side == 1 {
		return 2
	}
	return 1
}

func centreIdx() int { return (N/2)*N + N/2 }

// centreDist is the squared distance from idx to the board centre (tie-breaker).
func centreDist(idx int) int {
	dr, dc := idx/N-N/2, idx%N-N/2
	return dr*dr + dc*dc
}

// deque is a minimal int deque backed by a slice with a moving head, used for
// the 0-1 BFS (cost-0 edges push to the front, cost-1 edges to the back).
type deque struct {
	items []int
	head  int
}

func (d *deque) empty() bool { return d.head >= len(d.items) }

func (d *deque) popFront() int {
	v := d.items[d.head]
	d.head++
	return v
}

func (d *deque) pushCost(idx, w int) {
	if w == 0 {
		d.pushFront(idx)
		return
	}
	d.items = append(d.items, idx)
}

func (d *deque) pushFront(idx int) {
	if d.head > 0 {
		d.head--
		d.items[d.head] = idx
		return
	}
	d.items = append([]int{idx}, d.items...)
}
