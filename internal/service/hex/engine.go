// Hex (the connection game) rules — pure logic, no protocol. Two players place
// stones on an 11×11 rhombus; there are no captures and no draws — exactly one
// player ever connects their two edges.
package hex

import "errors"

// N is the board edge: the classic 11×11 Hex board.
const N = 11

// Board cells: 0 empty, 1 = red (P1, moves first), 2 = blue (P2).
// Index = row*N + col, row 0 at the top, col 0 at the left.
// Red (P1) connects the top edge (row 0) to the bottom edge (row N-1);
// blue (P2) connects the left edge (col 0) to the right edge (col N-1).
type Board [N * N]int8

var (
	ErrOffBoard = errors.New("hex: off the board")
	ErrOccupied = errors.New("hex: cell already taken")
)

// neighbourOffsets are the six Hex adjacencies on a rhombus board: the four
// orthogonal steps plus the two "short-diagonal" steps that make each cell
// hexagonal (up-right and down-left).
var neighbourOffsets = [6][2]int{
	{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, 1}, {1, -1},
}

func inBounds(row, col int) bool {
	return row >= 0 && row < N && col >= 0 && col < N
}

// Place puts a stone for side (1 or 2) at (row, col), returning its board index.
func (b *Board) Place(row, col, side int8) (int16, error) {
	if !inBounds(int(row), int(col)) {
		return 0, ErrOffBoard
	}
	idx := int(row)*N + int(col)
	if b[idx] != 0 {
		return 0, ErrOccupied
	}
	b[idx] = side
	return int16(idx), nil
}

// Winner returns the winning side (1/2) and the connected group of stones that
// spans that side's two edges, or (0, nil) if neither side has connected yet.
func (b *Board) Winner() (int8, []int16) {
	if cells := b.connected(1); cells != nil {
		return 1, cells
	}
	if cells := b.connected(2); cells != nil {
		return 2, cells
	}
	return 0, nil
}

// connected flood-fills side's stones from its start edge and returns the whole
// connected group if that group also touches its goal edge, else nil.
func (b *Board) connected(side int8) []int16 {
	visited := make([]bool, N*N)
	queue := b.seedEdge(side, visited)
	group := make([]int16, 0, len(queue))
	won := false
	for len(queue) > 0 {
		idx := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		group = append(group, int16(idx))
		if atGoalEdge(side, idx) {
			won = true
		}
		b.pushNeighbours(side, idx, visited, &queue)
	}
	if !won {
		return nil
	}
	return group
}

// seedEdge marks and returns every stone of side sitting on its start edge
// (row 0 for red, col 0 for blue).
func (b *Board) seedEdge(side int8, visited []bool) []int {
	var queue []int
	for i := 0; i < N; i++ {
		idx := i // red: row 0, col i
		if side == 2 {
			idx = i * N // blue: row i, col 0
		}
		if b[idx] == side {
			visited[idx] = true
			queue = append(queue, idx)
		}
	}
	return queue
}

// atGoalEdge reports whether idx sits on side's goal edge (bottom row for red,
// right column for blue).
func atGoalEdge(side int8, idx int) bool {
	if side == 1 {
		return idx/N == N-1
	}
	return idx%N == N-1
}

// pushNeighbours enqueues the unvisited same-side neighbours of idx.
func (b *Board) pushNeighbours(side int8, idx int, visited []bool, queue *[]int) {
	row, col := idx/N, idx%N
	for _, d := range neighbourOffsets {
		r, c := row+d[0], col+d[1]
		if !inBounds(r, c) {
			continue
		}
		n := r*N + c
		if !visited[n] && b[n] == side {
			visited[n] = true
			*queue = append(*queue, n)
		}
	}
}
