// Package bot is the solo "Play the computer" opponent. It drives one end of the
// in-memory solo loopback: it watches that end's merged event stream and, on
// each game state where it is the side to move, plays a move on that end's
// service. There is no separate AI engine — moves come from the legal moves the
// services already expose (or derive), and heuristics reuse each game engine's
// own simulation ops (connect4.Board.Drop/Winner, reversi.Place, checkers.Apply,
// backgammon.ApplyTurn; battleship places a random legal fleet and hunts/targets).
// Three levels: Easy (uniform random), Medium (mostly Hard with occasional random
// slips), and Hard (per-game heuristics). The seam for future difficulty tuning.
package bot

import (
	"math/rand"
	"time"

	"github.com/richardwooding/kibitz/internal/service/backgammon"
	"github.com/richardwooding/kibitz/internal/service/battleship"
	"github.com/richardwooding/kibitz/internal/service/checkers"
	"github.com/richardwooding/kibitz/internal/service/chess"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/kibitz/internal/service/dots"
	"github.com/richardwooding/kibitz/internal/service/gomoku"
	"github.com/richardwooding/kibitz/internal/service/hex"
	"github.com/richardwooding/kibitz/internal/service/reversi"
	"github.com/richardwooding/kibitz/internal/service/weiqi"
	"github.com/richardwooding/kibitz/internal/service/xiangqi"
	"github.com/richardwooding/kibitz/internal/shipcommit"
	"github.com/richardwooding/kibitz/internal/wire"
)

// Level is the bot's difficulty.
type Level int

const (
	Easy   Level = iota // uniform random legal move
	Medium              // mostly Hard, but slips to a random move sometimes
	Hard                // per-game heuristics
)

// mediumMistake is how often Medium slips from its best move to a random legal
// one — between Easy (always random) and Hard (always its best).
const mediumMistake = 0.4

// resolveLevel maps the configured level to the level used for a single move.
// Medium plays Hard most of the time but slips to Easy (random) with probability
// mediumMistake; r is a [0,1) draw (injected so it is testable).
func resolveLevel(level Level, r float64) Level {
	if level == Medium {
		if r < mediumMistake {
			return Easy
		}
		return Hard
	}
	return level
}

// Services is the one end the bot plays on: its game-service set plus the bot's
// own participant id (the side it moves for).
type Services struct {
	Self  wire.ParticipantID
	Chess *chess.Service
	BG    *backgammon.Service
	C4    *connect4.Service
	GM    *gomoku.Service
	HEX   *hex.Service
	DOTS  *dots.Service
	GO    *weiqi.Service
	XQ    *xiangqi.Service
	CK    *checkers.Service
	RV    *reversi.Service
	BS    *battleship.Service
}

// Drive consumes an end's merged event stream and plays the bot's move whenever
// that end is the side to move. It returns when the stream closes, so run it in
// a goroutine (it also drains the stream). delay paces each move for a natural
// feel (pass 0 in tests). Move methods are turn-checked by the services, so a
// stray call is simply rejected.
func Drive(events <-chan any, s Services, delay time.Duration, level Level) {
	pause := func() {
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	for ev := range events {
		switch e := ev.(type) {
		case connect4.State:
			driveConnect4(s, e, level, pause)
		case gomoku.State:
			driveGomoku(s, e, level, pause)
		case hex.State:
			driveHex(s, e, level, pause)
		case dots.State:
			driveDots(s, e, level, pause)
		case weiqi.State:
			driveWeiqi(s, e, level, pause)
		case xiangqi.State:
			driveXiangqi(s, e, level, pause)
		case reversi.State:
			driveReversi(s, e, level, pause)
		case checkers.State:
			driveCheckers(s, e, level, pause)
		case chess.State:
			driveChess(s, e, level, pause)
		case backgammon.State:
			driveBackgammon(s, e, level, pause)
		case battleship.State:
			driveBattleship(s, e, level, pause)
		}
	}
}

// Each drive<Game> plays the bot's move for one state event when it is the bot's
// turn. Extracted from Drive so that switch stays a thin dispatcher (its cognitive
// complexity was a hotspot) and each game's logic is testable in isolation.

func driveConnect4(s Services, e connect4.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self {
		return
	}
	disc := int8(1)
	if e.P2ID == s.Self {
		disc = 2
	}
	var col int8
	var ok bool
	if resolveLevel(level, rand.Float64()) == Hard {
		col, ok = c4Hard(e.Board, disc)
	} else {
		col, ok = c4Random(e.Board)
	}
	if ok {
		pause()
		_ = s.C4.Drop(col)
	}
}

func driveReversi(s Services, e reversi.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self || len(e.Legal) == 0 {
		return
	}
	side := int8(1) // black = P1
	if e.P2ID == s.Self {
		side = -1
	}
	pause()
	_ = s.RV.PlaceDisc(rvPick(resolveLevel(level, rand.Float64()), e.Board, e.Legal, side))
}

func driveCheckers(s Services, e checkers.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self || len(e.Legal) == 0 {
		return
	}
	side := checkers.Black
	if e.P2ID == s.Self {
		side = checkers.White
	}
	pause()
	_ = s.CK.TryMove([]int8(ckPick(resolveLevel(level, rand.Float64()), e.Board, e.Legal, side)))
}

func driveChess(s Services, e chess.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "*" || e.TurnID != s.Self {
		return
	}
	uci := ""
	if resolveLevel(level, rand.Float64()) == Hard {
		uci = s.Chess.HardMove() // alpha-beta material minimax
	} else if mv := s.Chess.LegalMoves(); len(mv) > 0 {
		uci = mv[rand.Intn(len(mv))]
	}
	if uci != "" {
		pause()
		_ = s.Chess.TryMove(uci)
	}
}

func driveBackgammon(s Services, e backgammon.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self {
		return
	}
	switch e.Phase {
	case "rolling":
		pause()
		_ = s.BG.Roll()
	case "moving":
		if len(e.Legal) == 0 {
			return
		}
		color := backgammon.White
		if e.BlackID == s.Self {
			color = backgammon.Black
		}
		pause()
		_ = s.BG.Move(bgPick(resolveLevel(level, rand.Float64()), e.Board, e.Legal, color))
	}
}

func driveBattleship(s Services, e battleship.State, level Level, pause func()) {
	if !e.Playing {
		return
	}
	mySide := bsSide(e, s.Self)
	if mySide < 0 {
		return // spectator
	}
	switch e.Phase {
	case "placing":
		bsPlace(s, e, mySide, pause)
	case "shooting":
		// TurnID is 0 while a shot awaits its reveal, so this waits.
		bsShoot(s, e, mySide, level, pause)
	}
}

func bsSide(e battleship.State, self wire.ParticipantID) int {
	if e.P1ID == self {
		return 0
	}
	if e.P2ID == self {
		return 1
	}
	return -1
}

func bsPlace(s Services, e battleship.State, mySide int, pause func()) {
	if e.Committed[mySide] {
		return
	}
	if fleet, err := shipcommit.RandomPlacement(); err == nil {
		pause()
		_ = s.BS.Commit(fleet)
	}
}

func bsShoot(s Services, e battleship.State, mySide int, level Level, pause func()) {
	if e.TurnID != s.Self {
		return
	}
	if cell, ok := bsTarget(e.Reveals[1-mySide], e.Sunk[1-mySide], resolveLevel(level, rand.Float64())); ok {
		pause()
		_ = s.BS.Shoot(cell)
	}
}

// ---- connect4 -------------------------------------------------------------

func c4Legal(b connect4.Board) []int8 {
	var out []int8
	for c := 0; c < connect4.Cols; c++ {
		if b[c*connect4.Rows+(connect4.Rows-1)] == 0 {
			out = append(out, int8(c))
		}
	}
	return out
}

func c4Random(b connect4.Board) (int8, bool) {
	legal := c4Legal(b)
	if len(legal) == 0 {
		return 0, false
	}
	return legal[rand.Intn(len(legal))], true
}

// c4Hard is an alpha-beta negamax over the connect4 engine's own Drop/Winner: it
// finds wins, blocks losses, and biases toward the centre, all within the search.
func c4Hard(board connect4.Board, disc int8) (int8, bool) {
	legal := c4Legal(board)
	if len(legal) == 0 {
		return 0, false
	}
	best, bestScore := legal[0], -1<<30
	for _, col := range legal {
		nb := board
		if _, err := nb.Drop(col, disc); err != nil {
			continue
		}
		score := -c4Negamax(&nb, c4Other(disc), 5, -1<<30, 1<<30)
		if score > bestScore {
			bestScore, best = score, col
		}
	}
	return best, true
}

// c4Negamax scores the position for `toMove` to play, depth plies ahead.
func c4Negamax(b *connect4.Board, toMove int8, depth, alpha, beta int) int {
	if w, _ := b.Winner(); w != 0 {
		// The player who just moved (opponent of toMove) has four in a row.
		return -100000 - depth // prefer wins sooner / losses later
	}
	if b.Full() || depth == 0 {
		return c4Eval(b, toMove)
	}
	best := -1 << 30
	for _, col := range c4Legal(*b) {
		nb := *b
		if _, err := nb.Drop(col, toMove); err != nil {
			continue
		}
		s := -c4Negamax(&nb, c4Other(toMove), depth-1, -beta, -alpha)
		if s > best {
			best = s
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

var c4ColWeight = [connect4.Cols]int{1, 2, 3, 4, 3, 2, 1}

// c4Eval is a cheap leaf heuristic: centre-weighted disc control for toMove.
func c4Eval(b *connect4.Board, toMove int8) int {
	score := 0
	for c := 0; c < connect4.Cols; c++ {
		for r := 0; r < connect4.Rows; r++ {
			v := b[c*connect4.Rows+r]
			if v == toMove {
				score += c4ColWeight[c]
			} else if v != 0 {
				score -= c4ColWeight[c]
			}
		}
	}
	return score
}

func c4Other(d int8) int8 {
	if d == 1 {
		return 2
	}
	return 1
}

// ---- gomoku ---------------------------------------------------------------

// driveGomoku plays the bot's stone when it is on turn (extracted from Drive to
// keep that switch's cognitive complexity within the ratchet).
func driveGomoku(s Services, e gomoku.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self {
		return
	}
	side := int8(1) // black = P1
	if e.P2ID == s.Self {
		side = 2
	}
	if row, col, ok := gmPick(resolveLevel(level, rand.Float64()), e.Board, side); ok {
		pause()
		_ = s.GM.Place(row, col)
	}
}

func gmPick(level Level, b gomoku.Board, side int8) (int8, int8, bool) {
	if level == Hard {
		return gmHard(b, side)
	}
	return gmRandom(b)
}

func gmRandom(b gomoku.Board) (int8, int8, bool) {
	var empty []int
	for i := 0; i < len(b); i++ {
		if b[i] == 0 {
			empty = append(empty, i)
		}
	}
	if len(empty) == 0 {
		return 0, 0, false
	}
	i := empty[rand.Intn(len(empty))]
	return int8(i / gomoku.Size), int8(i % gomoku.Size), true
}

// gmHard scores every empty cell near a stone by the threat it makes for the
// bot plus the threat it denies the opponent (blocking), and plays the best —
// so it completes and blocks fours/open-threes without a full tree search.
func gmHard(b gomoku.Board, side int8) (int8, int8, bool) {
	opp := int8(1)
	if side == 1 {
		opp = 2
	}
	best, bestScore := -1, -1
	for _, idx := range gmCandidates(b) {
		if score := gmEval(b, idx, side) + gmEval(b, idx, opp); score > bestScore {
			bestScore, best = score, idx
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	return int8(best / gomoku.Size), int8(best % gomoku.Size), true
}

// gmCandidates lists empty cells within two of any stone (the only moves worth
// considering); on an empty board it returns just the centre.
func gmCandidates(b gomoku.Board) []int {
	var out []int
	any := false
	for i := 0; i < len(b); i++ {
		if b[i] != 0 {
			any = true
			continue
		}
		if gmNearStone(b, i) {
			out = append(out, i)
		}
	}
	if !any {
		return []int{(gomoku.Size/2)*gomoku.Size + gomoku.Size/2}
	}
	return out
}

func gmNearStone(b gomoku.Board, idx int) bool {
	row, col := idx/gomoku.Size, idx%gomoku.Size
	for dr := -2; dr <= 2; dr++ {
		for dc := -2; dc <= 2; dc++ {
			if (dr != 0 || dc != 0) && gmCell(b, row+dr, col+dc) > 0 {
				return true
			}
		}
	}
	return false
}

// gmCell reads a board cell, returning -1 off the board (so run scans stop).
func gmCell(b gomoku.Board, r, c int) int8 {
	if r < 0 || r >= gomoku.Size || c < 0 || c >= gomoku.Size {
		return -1
	}
	return b[r*gomoku.Size+c]
}

// gmEval scores placing `who` at idx: the sum over the four lines of the run it
// would make, weighted by length and open ends.
func gmEval(b gomoku.Board, idx int, who int8) int {
	row, col := idx/gomoku.Size, idx%gomoku.Size
	dirs := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	score := 0
	for _, d := range dirs {
		score += gmDirScore(b, row, col, d[0], d[1], who)
	}
	return score
}

func gmDirScore(b gomoku.Board, row, col, dr, dc int, who int8) int {
	count, open := 1, 0
	r, c := row+dr, col+dc
	for gmCell(b, r, c) == who {
		count++
		r += dr
		c += dc
	}
	if gmCell(b, r, c) == 0 {
		open++
	}
	r, c = row-dr, col-dc
	for gmCell(b, r, c) == who {
		count++
		r -= dr
		c -= dc
	}
	if gmCell(b, r, c) == 0 {
		open++
	}
	return gmPattern(count, open)
}

// gmPattern maps a run's length and open-end count to a heuristic value: a made
// five is decisive, an open four or double threat is strong, blocked runs worth
// little.
func gmPattern(count, open int) int {
	if count >= 5 {
		return 1000000
	}
	if open == 0 {
		return 0
	}
	switch count {
	case 4:
		if open == 2 {
			return 100000
		}
		return 10000
	case 3:
		if open == 2 {
			return 5000
		}
		return 500
	case 2:
		if open == 2 {
			return 100
		}
		return 10
	}
	return 1
}

// ---- hex / dots / weiqi / xiangqi -----------------------------------------
// Hard routes to each engine's BestMove (search/heuristic); Easy plays a random
// legal move; Medium mixes the two via resolveLevel.

func driveHex(s Services, e hex.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self || len(e.Legal) == 0 {
		return
	}
	if resolveLevel(level, rand.Float64()) == Hard {
		side := int8(1) // red = P1, blue = P2
		if e.P2ID == s.Self {
			side = 2
		}
		if row, col, ok := hex.BestMove(e.Board, side); ok {
			pause()
			_ = s.HEX.Place(row, col)
		}
		return
	}
	idx := int(e.Legal[rand.Intn(len(e.Legal))])
	pause()
	_ = s.HEX.Place(int8(idx/hex.N), int8(idx%hex.N))
}

func driveDots(s Services, e dots.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self || len(e.Legal) == 0 {
		return
	}
	if resolveLevel(level, rand.Float64()) == Hard {
		if edge, ok := dots.BestMove(dots.Board{Edges: e.Edges, Owner: e.Boxes}, rand.Int()); ok {
			pause()
			_ = s.DOTS.DrawEdge(edge)
		}
		return
	}
	pause()
	_ = s.DOTS.DrawEdge(e.Legal[rand.Intn(len(e.Legal))])
}

func driveWeiqi(s Services, e weiqi.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self {
		return
	}
	if resolveLevel(level, rand.Float64()) == Hard {
		side := int8(1) // black = P1, white = P2
		if e.P2ID == s.Self {
			side = 2
		}
		row, col, pass := weiqi.BestMove(e.Board, side)
		pause()
		if pass {
			_ = s.GO.Pass()
		} else {
			_ = s.GO.Place(row, col)
		}
		return
	}
	pause()
	// Easy: random point, or pass when out of points / ~1-in-20 to avoid dragging.
	if len(e.Legal) == 0 || rand.Intn(20) == 0 {
		_ = s.GO.Pass()
		return
	}
	m := int(e.Legal[rand.Intn(len(e.Legal))])
	_ = s.GO.Place(int8(m/weiqi.N), int8(m%weiqi.N))
}

func driveXiangqi(s Services, e xiangqi.State, level Level, pause func()) {
	if !e.Playing || e.Outcome != "" || e.TurnID != s.Self || len(e.Legal) == 0 {
		return
	}
	if resolveLevel(level, rand.Float64()) == Hard {
		side := int8(1) // red = P1 (+1), black = P2 (-1)
		if e.P2ID == s.Self {
			side = -1
		}
		if from, to, ok := xiangqi.BestMove(e.Board, side); ok {
			pause()
			_ = s.XQ.Move(from, to)
		}
		return
	}
	mv := e.Legal[rand.Intn(len(e.Legal))]
	pause()
	_ = s.XQ.Move(mv[0], mv[1])
}

// ---- reversi --------------------------------------------------------------

// rvWeights is the classic Othello positional table (row-major 8x8): corners
// dominate, squares next to an empty corner are traps.
var rvWeights = [64]int{
	100, -20, 10, 5, 5, 10, -20, 100,
	-20, -50, -2, -2, -2, -2, -50, -20,
	10, -2, -1, -1, -1, -1, -2, 10,
	5, -2, -1, -1, -1, -1, -2, 5,
	5, -2, -1, -1, -1, -1, -2, 5,
	10, -2, -1, -1, -1, -1, -2, 10,
	-20, -50, -2, -2, -2, -2, -50, -20,
	100, -20, 10, 5, 5, 10, -20, 100,
}

func rvPick(level Level, board reversi.Board, legal []int8, side int8) int8 {
	if level != Hard {
		return legal[rand.Intn(len(legal))]
	}
	best, bestScore := legal[0], -1<<30
	for _, sq := range legal {
		nb, err := reversi.Place(board, side, sq)
		if err != nil {
			continue
		}
		s := 0
		for i, v := range nb {
			switch v {
			case side:
				s += rvWeights[i]
			case -side:
				s -= rvWeights[i]
			}
		}
		if s > bestScore {
			bestScore, best = s, sq
		}
	}
	return best
}

// ---- checkers -------------------------------------------------------------

func ckPick(level Level, board checkers.Board, legal []checkers.Move, side checkers.Side) checkers.Move {
	if level != Hard {
		return legal[rand.Intn(len(legal))]
	}
	best, bestScore := []checkers.Move{legal[0]}, -1<<30
	for _, m := range legal {
		s := ckMaterial(checkers.Apply(board, side, m), side)
		switch {
		case s > bestScore:
			bestScore, best = s, []checkers.Move{m}
		case s == bestScore:
			best = append(best, m)
		}
	}
	return best[rand.Intn(len(best))]
}

// ckMaterial scores own minus opponent material (man 1, king 2) from side's view.
// Board: +1/+2 = black (P1), -1/-2 = white (P2).
func ckMaterial(b checkers.Board, side checkers.Side) int {
	ownBlack := side == checkers.Black
	score := 0
	for _, c := range b {
		if c == 0 {
			continue
		}
		val := 1
		if c == 2 || c == -2 {
			val = 2
		}
		if (c > 0) == ownBlack {
			score += val
		} else {
			score -= val
		}
	}
	return score
}

// ---- backgammon -----------------------------------------------------------

func bgPick(level Level, board backgammon.Board, legal [][]backgammon.Hop, color backgammon.Color) []backgammon.Hop {
	if level != Hard {
		return legal[rand.Intn(len(legal))]
	}
	best, bestScore := legal[0], -1<<30
	for _, turn := range legal {
		s := bgEval(backgammon.ApplyTurn(board, color, turn), color)
		if s > bestScore {
			bestScore, best = s, turn
		}
	}
	return best
}

// bgEval favours a lower own pip count, sending opponents to the bar (hits), and
// leaving few of its own blots (lone checkers a hit can send back).
func bgEval(b backgammon.Board, color backgammon.Color) int {
	opp := color.Opponent()
	hits := int(b.Bar[opp])
	blots := 0
	for p := 1; p <= 24; p++ {
		n := int(b.Points[p])
		own := n // positive = White
		if color == backgammon.Black {
			own = -n
		}
		if own == 1 {
			blots++
		}
	}
	return -b.PipCount(color) + 25*hits - 4*blots
}

// Chess Hard is a material minimax that lives in the chess service (it owns the
// corentings/chess position); the bot calls s.Chess.HardMove(). Easy plays a
// random legal move (handled inline in Drive).

// ---- battleship -----------------------------------------------------------

// bsTarget chooses a cell (0..99) to shoot from the bot's view of the opponent
// board: shots[cell] is -1 un-shot, 0 miss, 1..5 a hit ship id; sunk lists the
// fully-sunk ship ids. Easy fires at a random un-shot cell; Hard hunts and
// targets — it finishes off a struck-but-unsunk ship by firing at un-shot cells
// next to it, otherwise hunts on the checkerboard parity (every ship covers a
// parity cell, so half the board suffices to find them all).
func bsTarget(shots [100]int8, sunk []uint8, level Level) (uint8, bool) {
	if level != Hard {
		return bsRandom(shots, func(int) bool { return true })
	}
	if targets := bsHuntTargets(shots, sunk); len(targets) > 0 {
		return uint8(targets[rand.Intn(len(targets))]), true
	}
	if cell, ok := bsRandom(shots, func(c int) bool { return (c%10+c/10)%2 == 0 }); ok {
		return cell, true // hunt on parity
	}
	return bsRandom(shots, func(int) bool { return true })
}

// bsHuntTargets lists un-shot cells orthogonally adjacent to a struck but
// not-yet-sunk ship — the "target" phase after a hunt lands a hit.
func bsHuntTargets(shots [100]int8, sunk []uint8) []int {
	isSunk := func(id int8) bool {
		for _, s := range sunk {
			if int8(s) == id {
				return true
			}
		}
		return false
	}
	var targets []int
	for c := 0; c < 100; c++ {
		if shots[c] < 1 || isSunk(shots[c]) {
			continue // not a live (unsunk) hit
		}
		x, y := c%10, c/10
		for _, n := range neighbours(x, y) {
			if shots[n] == -1 {
				targets = append(targets, n)
			}
		}
	}
	return targets
}

// neighbours returns the on-board orthogonal neighbour cells of (x,y).
func neighbours(x, y int) []int {
	var out []int
	if y > 0 {
		out = append(out, (y-1)*10+x)
	}
	if y < 9 {
		out = append(out, (y+1)*10+x)
	}
	if x > 0 {
		out = append(out, y*10+x-1)
	}
	if x < 9 {
		out = append(out, y*10+x+1)
	}
	return out
}

// bsRandom returns a random un-shot cell satisfying pred, or false if none.
func bsRandom(shots [100]int8, pred func(int) bool) (uint8, bool) {
	var cs []int
	for c := 0; c < 100; c++ {
		if shots[c] == -1 && pred(c) {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return 0, false
	}
	return uint8(cs[rand.Intn(len(cs))]), true
}
