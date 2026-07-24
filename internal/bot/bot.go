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
	"github.com/richardwooding/kibitz/internal/service/reversi"
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
			if e.Playing && e.Outcome == "" && e.TurnID == s.Self {
				disc := int8(1)
				if e.P2ID == s.Self {
					disc = 2
				}
				col, ok := int8(-1), false
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
		case reversi.State:
			if e.Playing && e.Outcome == "" && e.TurnID == s.Self && len(e.Legal) > 0 {
				side := int8(1) // black = P1
				if e.P2ID == s.Self {
					side = -1
				}
				pause()
				_ = s.RV.PlaceDisc(rvPick(resolveLevel(level, rand.Float64()), e.Board, e.Legal, side))
			}
		case checkers.State:
			if e.Playing && e.Outcome == "" && e.TurnID == s.Self && len(e.Legal) > 0 {
				side := checkers.Black
				if e.P2ID == s.Self {
					side = checkers.White
				}
				pause()
				_ = s.CK.TryMove([]int8(ckPick(resolveLevel(level, rand.Float64()), e.Board, e.Legal, side)))
			}
		case chess.State:
			if e.Playing && e.Outcome == "*" && e.TurnID == s.Self {
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
		case backgammon.State:
			if e.Playing && e.Outcome == "" && e.TurnID == s.Self {
				switch e.Phase {
				case "rolling":
					pause()
					_ = s.BG.Roll()
				case "moving":
					if len(e.Legal) > 0 {
						color := backgammon.White
						if e.BlackID == s.Self {
							color = backgammon.Black
						}
						pause()
						_ = s.BG.Move(bgPick(resolveLevel(level, rand.Float64()), e.Board, e.Legal, color))
					}
				}
			}
		case battleship.State:
			if !e.Playing {
				break
			}
			mySide := -1
			if e.P1ID == s.Self {
				mySide = 0
			} else if e.P2ID == s.Self {
				mySide = 1
			}
			if mySide < 0 {
				break // spectator
			}
			switch e.Phase {
			case "placing":
				if !e.Committed[mySide] {
					if fleet, err := shipcommit.RandomPlacement(); err == nil {
						pause()
						_ = s.BS.Commit(fleet)
					}
				}
			case "shooting":
				// TurnID is 0 while a shot awaits its reveal, so this waits.
				if e.TurnID == s.Self {
					if cell, ok := bsTarget(e.Reveals[1-mySide], e.Sunk[1-mySide], resolveLevel(level, rand.Float64())); ok {
						pause()
						_ = s.BS.Shoot(cell)
					}
				}
			}
		}
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
			if v == side {
				s += rvWeights[i]
			} else if v == -side {
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
	if len(targets) > 0 {
		return uint8(targets[rand.Intn(len(targets))]), true
	}
	if cell, ok := bsRandom(shots, func(c int) bool { return (c%10+c/10)%2 == 0 }); ok {
		return cell, true // hunt on parity
	}
	return bsRandom(shots, func(int) bool { return true })
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
