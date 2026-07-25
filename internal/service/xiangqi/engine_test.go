package xiangqi

import (
	"errors"
	"testing"
)

// hasMove reports whether {from,to} is in the move list.
func hasMove(moves [][2]int8, from, to int8) bool {
	for _, m := range moves {
		if m[0] == from && m[1] == to {
			return true
		}
	}
	return false
}

// movesFrom returns the destination set of pseudo-legal moves from a square.
func movesFrom(b Board, from int8) map[int8]bool {
	out := map[int8]bool{}
	for _, t := range pieceMoves(b, from) {
		out[t] = true
	}
	return out
}

// pieceCounts returns the number of red and black pieces on the board.
func pieceCounts(b Board) (red, black int) {
	for _, v := range b {
		switch {
		case v > 0:
			red++
		case v < 0:
			black++
		}
	}
	return red, black
}

func TestStartPosition(t *testing.T) {
	b := Start()
	// Red back rank.
	if b[idxOf(0, 0)] != Chariot || b[idxOf(0, 4)] != General || b[idxOf(0, 8)] != Chariot {
		t.Fatalf("red back rank wrong: %v", b[:9])
	}
	// Black back rank is the mirror (negated).
	if b[idxOf(9, 4)] != -General || b[idxOf(9, 1)] != -Horse {
		t.Fatalf("black back rank wrong")
	}
	// Cannons and soldiers.
	if b[idxOf(2, 1)] != Cannon || b[idxOf(7, 7)] != -Cannon {
		t.Fatalf("cannons misplaced")
	}
	if b[idxOf(3, 0)] != Soldier || b[idxOf(3, 4)] != Soldier || b[idxOf(6, 8)] != -Soldier {
		t.Fatalf("soldiers misplaced")
	}
	// 32 pieces total, 16 each.
	red, black := pieceCounts(b)
	if red != 16 || black != 16 {
		t.Fatalf("piece counts red=%d black=%d", red, black)
	}
	// Opening has legal moves for both sides and nobody is in check.
	if InCheck(b, Red) || InCheck(b, Black) {
		t.Fatalf("opening should not be check")
	}
	if _, over := Winner(b, Red); over {
		t.Fatalf("opening is not game over")
	}
}

func TestChariotSlide(t *testing.T) {
	var b Board
	b[idxOf(0, 4)] = General  // keep generals on board for check logic
	b[idxOf(9, 0)] = -General // off file 4 to avoid flying-general
	c := idxOf(4, 4)
	b[c] = Chariot
	b[idxOf(4, 7)] = -Soldier // enemy to capture along the rank
	b[idxOf(7, 4)] = Chariot  // friendly blocker up the file
	m := movesFrom(b, c)
	if !m[idxOf(4, 5)] || !m[idxOf(4, 6)] || !m[idxOf(4, 7)] { // slides then captures
		t.Fatalf("chariot should slide east and capture at (4,7)")
	}
	if m[idxOf(4, 8)] { // cannot pass through the captured piece
		t.Fatalf("chariot should not pass the captured piece")
	}
	if m[idxOf(7, 4)] || m[idxOf(8, 4)] { // cannot capture/pass friendly
		t.Fatalf("chariot should stop before the friendly chariot")
	}
	if !m[idxOf(5, 4)] || !m[idxOf(6, 4)] {
		t.Fatalf("chariot should reach (5,4) and (6,4)")
	}
}

func TestHorseLegBlock(t *testing.T) {
	var b Board
	h := idxOf(4, 4)
	b[h] = Horse
	// Unblocked: all 8 targets available.
	if len(pieceMoves(b, h)) != 8 {
		t.Fatalf("open horse should have 8 moves, got %d", len(pieceMoves(b, h)))
	}
	// Block the leg directly north: kills the two "up" destinations.
	b[idxOf(5, 4)] = Soldier
	m := movesFrom(b, h)
	if m[idxOf(6, 5)] || m[idxOf(6, 3)] {
		t.Fatalf("blocked north leg should remove (6,5) and (6,3)")
	}
	if !m[idxOf(2, 5)] || !m[idxOf(5, 6)] {
		t.Fatalf("other horse moves should remain")
	}
}

func TestElephantEyeAndRiver(t *testing.T) {
	var b Board
	e := idxOf(2, 2) // a red elephant square
	b[e] = Elephant
	m := movesFrom(b, e)
	// Reaches (0,0),(0,4),(4,0),(4,4); never crosses river (rank>4).
	if !m[idxOf(0, 0)] || !m[idxOf(4, 4)] || !m[idxOf(0, 4)] || !m[idxOf(4, 0)] {
		t.Fatalf("elephant should reach all four 2-step diagonals on its half")
	}
	// Block the eye toward (4,4): removes that destination only.
	b[idxOf(3, 3)] = Soldier
	m = movesFrom(b, e)
	if m[idxOf(4, 4)] {
		t.Fatalf("blocked elephant eye should remove (4,4)")
	}
	if !m[idxOf(0, 0)] {
		t.Fatalf("other elephant diagonal should remain")
	}
	// An elephant one row below the river cannot cross it.
	var c Board
	c[idxOf(4, 2)] = Elephant
	mc := movesFrom(c, idxOf(4, 2))
	if mc[idxOf(6, 4)] || mc[idxOf(6, 0)] {
		t.Fatalf("elephant must not cross the river")
	}
	if !mc[idxOf(2, 4)] || !mc[idxOf(2, 0)] {
		t.Fatalf("elephant should still move back on its own half")
	}
}

func TestCannonScreenCaptureVsMove(t *testing.T) {
	var b Board
	c := idxOf(4, 4)
	b[c] = Cannon
	b[idxOf(4, 6)] = Soldier  // screen (friendly, irrelevant to the jump)
	b[idxOf(4, 8)] = -Soldier // target beyond exactly one screen
	b[idxOf(2, 4)] = -Chariot // directly reachable? no screen below → quiet only
	m := movesFrom(b, c)
	// Quiet moves along the empty part of the east ray up to the screen.
	if !m[idxOf(4, 5)] {
		t.Fatalf("cannon should be able to move to empty (4,5)")
	}
	if m[idxOf(4, 6)] {
		t.Fatalf("cannon cannot move onto the screen without a jump")
	}
	// Capture over exactly one screen.
	if !m[idxOf(4, 8)] {
		t.Fatalf("cannon should capture (4,8) over one screen")
	}
	// Downward: no screen between cannon and the enemy chariot → it may only
	// move to the empty square in front, NOT capture the chariot.
	if m[idxOf(2, 4)] {
		t.Fatalf("cannon must not capture with zero screens")
	}
	if !m[idxOf(3, 4)] {
		t.Fatalf("cannon should move to empty (3,4)")
	}
	// Two screens: add a piece between screen and target → capture blocked.
	b[idxOf(4, 7)] = Soldier
	m = movesFrom(b, c)
	if m[idxOf(4, 8)] {
		t.Fatalf("cannon must not capture over two screens")
	}
}

func TestSoldierPrePostRiver(t *testing.T) {
	var b Board
	// Red soldier before the river (rank 3): forward only.
	s := idxOf(3, 4)
	b[s] = Soldier
	m := movesFrom(b, s)
	if !m[idxOf(4, 4)] {
		t.Fatalf("red soldier should step forward to (4,4)")
	}
	if m[idxOf(3, 3)] || m[idxOf(3, 5)] || m[idxOf(2, 4)] {
		t.Fatalf("pre-river soldier must not step sideways or back")
	}
	// Red soldier past the river (rank 6): forward and sideways, never back.
	var c Board
	p := idxOf(6, 4)
	c[p] = Soldier
	mc := movesFrom(c, p)
	if !mc[idxOf(7, 4)] || !mc[idxOf(6, 3)] || !mc[idxOf(6, 5)] {
		t.Fatalf("post-river soldier should move forward and sideways")
	}
	if mc[idxOf(5, 4)] {
		t.Fatalf("soldier must never move backward")
	}
	// Black soldier moves the other way (decreasing rank).
	var d Board
	q := idxOf(6, 4)
	d[q] = -Soldier
	md := movesFrom(d, q)
	if !md[idxOf(5, 4)] {
		t.Fatalf("black soldier should step forward to (5,4)")
	}
	if md[idxOf(7, 4)] {
		t.Fatalf("black soldier must not move backward")
	}
}

func TestAdvisorAndGeneralPalace(t *testing.T) {
	var b Board
	// General at palace centre: four orthogonal steps, all in palace.
	g := idxOf(1, 4)
	b[g] = General
	if len(pieceMoves(b, g)) != 4 {
		t.Fatalf("central general should have 4 moves")
	}
	// General at a corner of the palace: only two moves stay inside.
	var c Board
	gc := idxOf(0, 3)
	c[gc] = General
	mc := movesFrom(c, gc)
	if !mc[idxOf(0, 4)] || !mc[idxOf(1, 3)] {
		t.Fatalf("corner general should reach (0,4) and (1,3)")
	}
	if mc[idxOf(0, 2)] {
		t.Fatalf("general must stay in palace files")
	}
	if len(pieceMoves(c, gc)) != 2 {
		t.Fatalf("corner general should have exactly 2 moves, got %d", len(pieceMoves(c, gc)))
	}
	// Advisor moves diagonally within the palace only.
	var d Board
	a := idxOf(1, 4)
	d[a] = Advisor
	if len(pieceMoves(d, a)) != 4 {
		t.Fatalf("central advisor should have 4 diagonal moves")
	}
	// Advisor in a palace corner has one legal diagonal (to the centre).
	var e Board
	ac := idxOf(0, 3)
	e[ac] = Advisor
	me := movesFrom(e, ac)
	if !me[idxOf(1, 4)] || len(pieceMoves(e, ac)) != 1 {
		t.Fatalf("corner advisor should only reach the palace centre")
	}
}

func TestFlyingGeneralIllegal(t *testing.T) {
	var b Board
	b[idxOf(0, 4)] = General
	b[idxOf(9, 4)] = -General // same file, nothing between → facing
	if !generalsFacing(b) {
		t.Fatalf("generals on an open file should be facing")
	}
	if !InCheck(b, Red) || !InCheck(b, Black) {
		t.Fatalf("facing generals means both are in check")
	}
	// Put a piece between → no longer facing.
	b[idxOf(4, 4)] = Soldier
	if generalsFacing(b) {
		t.Fatalf("a screening piece should break the facing")
	}
	// A red move that opens the file (moving the screen off it) is illegal.
	var c Board
	c[idxOf(0, 4)] = General
	c[idxOf(9, 4)] = -General
	c[idxOf(4, 4)] = Cannon // red cannon shielding its own general on file 4
	if err := Validate(c, Red, idxOf(4, 4), idxOf(4, 0)); !errors.Is(err, ErrLeavesInCheck) {
		t.Fatalf("moving the shield off the file should be illegal, got %v", err)
	}
	// Sliding the cannon along the same file keeps the shield → legal.
	if err := Validate(c, Red, idxOf(4, 4), idxOf(5, 4)); err != nil {
		t.Fatalf("moving along the file should be legal, got %v", err)
	}
}

func TestPinnedPieceCannotExposeGeneral(t *testing.T) {
	var b Board
	b[idxOf(9, 4)] = -General // black general, file 4
	b[idxOf(0, 3)] = General  // red general off file 4
	b[idxOf(0, 4)] = Chariot  // red chariot down file 4
	b[idxOf(5, 4)] = -Chariot // black chariot pinned on file 4
	// Currently not in check: the pin blocks the red chariot.
	if InCheck(b, Black) {
		t.Fatalf("black should not be in check while the pin holds")
	}
	// Moving the pinned chariot off the file exposes the general → illegal.
	if err := Validate(b, Black, idxOf(5, 4), idxOf(5, 0)); !errors.Is(err, ErrLeavesInCheck) {
		t.Fatalf("pinned chariot leaving the file should be illegal, got %v", err)
	}
	// Moving it along the file (staying as a screen) is legal.
	if err := Validate(b, Black, idxOf(5, 4), idxOf(4, 4)); err != nil {
		t.Fatalf("pinned chariot may move along the file, got %v", err)
	}
}

func TestValidateBasics(t *testing.T) {
	b := Start()
	if err := Validate(b, Red, -1, 0); !errors.Is(err, ErrOffBoard) {
		t.Fatalf("off-board from: %v", err)
	}
	if err := Validate(b, Red, idxOf(9, 0), idxOf(8, 0)); !errors.Is(err, ErrNotYourPiece) {
		t.Fatalf("moving an enemy piece should fail: %v", err)
	}
	if err := Validate(b, Red, idxOf(0, 0), idxOf(3, 0)); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("chariot landing on its own soldier should be illegal: %v", err)
	}
	if err := Validate(b, Red, idxOf(0, 0), idxOf(4, 0)); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("chariot cannot pass its own soldier: %v", err)
	}
	// A legal opening chariot step (up file 0 one rank onto the empty point).
	if err := Validate(b, Red, idxOf(0, 0), idxOf(1, 0)); err != nil {
		t.Fatalf("chariot to (1,0) should be legal: %v", err)
	}
}

func TestCheckmate(t *testing.T) {
	var b Board
	b[idxOf(0, 4)] = General  // red general (kept off the mating files)
	b[idxOf(9, 3)] = -General // black general cornered in its palace
	b[idxOf(9, 0)] = Chariot  // controls rank 9
	b[idxOf(0, 3)] = Chariot  // controls file 3
	// Black to move: in check from the file-3 chariot, no escape or block.
	if !InCheck(b, Black) {
		t.Fatalf("black general should be in check")
	}
	if len(LegalMoves(b, Black)) != 0 {
		t.Fatalf("position should be checkmate, moves: %v", LegalMoves(b, Black))
	}
	w, over := Winner(b, Black)
	if !over || w != Red {
		t.Fatalf("expected red win, got winner=%d over=%v", w, over)
	}
}

func TestNotCheckmateWhenCaptureEscapes(t *testing.T) {
	var b Board
	b[idxOf(0, 4)] = General
	b[idxOf(9, 3)] = -General
	b[idxOf(8, 3)] = Chariot // gives check on file 3, but adjacent to the general
	// The general can simply capture the checking chariot (undefended).
	if !InCheck(b, Black) {
		t.Fatalf("black should be in check")
	}
	if !hasMove(LegalMoves(b, Black), idxOf(9, 3), idxOf(8, 3)) {
		t.Fatalf("general should be able to capture the checker")
	}
	if _, over := Winner(b, Black); over {
		t.Fatalf("not checkmate: the general escapes by capture")
	}
}

func TestApplyDoesNotMutate(t *testing.T) {
	b := Start()
	nb := Apply(b, idxOf(0, 0), idxOf(1, 0))
	if b[idxOf(0, 0)] == 0 {
		t.Fatalf("Apply must not mutate the input board")
	}
	if nb[idxOf(1, 0)] != Chariot || nb[idxOf(0, 0)] != 0 {
		t.Fatalf("Apply result wrong")
	}
}
