package xiangqi

import "testing"

// inLegal reports whether {from,to} is one of side's engine-legal moves.
func inLegal(b Board, side, from, to int8) bool {
	for _, m := range LegalMoves(b, side) {
		if m[0] == from && m[1] == to {
			return true
		}
	}
	return false
}

// TestBestMoveTakesFreeChariot: a red Chariot can capture an undefended black
// Chariot for a clean +900; the bot must take it.
func TestBestMoveTakesFreeChariot(t *testing.T) {
	var b Board
	b[idxOf(0, 0)] = Chariot  // red chariot a1
	b[idxOf(0, 4)] = General  // red general e1
	b[idxOf(4, 0)] = -Chariot // black chariot a5 (undefended)
	b[idxOf(9, 3)] = -General // black general d10

	from, to, ok := BestMove(b, Red)
	if !ok {
		t.Fatal("BestMove: expected a move, got ok=false")
	}
	if !inLegal(b, Red, from, to) {
		t.Fatalf("BestMove returned illegal move %d->%d", from, to)
	}
	if from != idxOf(0, 0) || to != idxOf(4, 0) {
		t.Fatalf("expected free chariot capture %d->%d, got %d->%d",
			idxOf(0, 0), idxOf(4, 0), from, to)
	}
}

// TestBestMoveDeliversMate: red has a forced mate in one (two chariots trap the
// black general). The bot's move must leave black lost — no legal reply, which
// the engine (Winner) scores as a win for the side that just moved.
func TestBestMoveDeliversMate(t *testing.T) {
	var b Board
	b[idxOf(0, 4)] = General  // red general e1 (kept on board so red moves are legal)
	b[idxOf(7, 8)] = Chariot  // red chariot: swings to rank 9 for the mate
	b[idxOf(8, 0)] = Chariot  // red chariot covering rank 9's escape (d9)
	b[idxOf(9, 3)] = -General // black general d10

	from, to, ok := BestMove(b, Red)
	if !ok {
		t.Fatal("BestMove: expected a move, got ok=false")
	}
	if !inLegal(b, Red, from, to) {
		t.Fatalf("BestMove returned illegal move %d->%d", from, to)
	}
	after := Apply(b, from, to)
	winner, over := Winner(after, Black)
	if !over || winner != Red {
		t.Fatalf("move %d->%d is not mate: Winner=(%d,%v), black still has %d replies",
			from, to, winner, over, len(LegalMoves(after, Black)))
	}
}

// TestBestMoveIsAlwaysLegal: from the opening the bot's move must be a member
// of the engine's legal set (so it can never be illegal or self-checking).
func TestBestMoveIsAlwaysLegal(t *testing.T) {
	b := Start()
	from, to, ok := BestMove(b, Red)
	if !ok {
		t.Fatal("BestMove: expected a move from the opening, got ok=false")
	}
	if !inLegal(b, Red, from, to) {
		t.Fatalf("BestMove returned illegal opening move %d->%d", from, to)
	}
}

// TestBestMoveNoMoves: a mated side has no move and BestMove reports ok=false.
func TestBestMoveNoMoves(t *testing.T) {
	var b Board
	b[idxOf(0, 3)] = General  // red general d1, checkmated below
	b[idxOf(0, 8)] = -Chariot // black chariot on rank 0 (checks along the rank)
	b[idxOf(9, 3)] = -Chariot // black chariot on file 3 (covers d2 escape)
	b[idxOf(9, 5)] = -General // black general f10

	if _, _, ok := BestMove(b, Red); ok {
		t.Fatal("BestMove: expected ok=false for a side with no legal move")
	}
}
