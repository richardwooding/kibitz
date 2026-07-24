package chess

import (
	"math/rand"
	"sort"

	chesslib "github.com/corentings/chess/v2"
)

// The solo "Play the computer" bot's Hard move for chess: an alpha-beta negamax
// with quiescence (captures settled at the horizon) over an evaluation of
// material plus piece-square positional bonuses. It searches a CLONE of the
// position (corentings/chess positions are immutable — Update returns a new
// one), so it never touches the live game; captures are searched first for
// pruning. It finds short tactics, won't hang a piece to a recapture, and plays
// with positional sense — including a game-phase king table so the king hides in
// the middlegame but centralises in the endgame.

const (
	botDepth = 4       // plies of lookahead
	botInf   = 1 << 30 // search infinity (> any score)
	botMate  = 1 << 20 // checkmate magnitude (≫ any material total)
)

// matValue is indexed by chesslib.PieceType: NoPieceType, King, Queen, Rook,
// Bishop, Knight, Pawn. The king has no material value (it is never captured).
var matValue = [7]int{0, 0, 900, 500, 330, 320, 100}

// HardMove returns the bot's chosen move in UCI for the current position, or ""
// if there is no game or no legal move. Safe to call on a live game.
func (s *Service) HardMove() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.game == nil {
		return ""
	}
	return bestMaterialMove(s.game.Position(), botDepth)
}

// bestMaterialMove picks the top move by alpha-beta negamax to depth plies.
// Root moves are shuffled then capture-ordered, so among equally-best moves the
// choice varies game to game (the first max found wins under alpha-beta).
func bestMaterialMove(pos *chesslib.Position, depth int) string {
	moves := pos.ValidMoves()
	if len(moves) == 0 {
		return ""
	}
	rand.Shuffle(len(moves), func(i, j int) { moves[i], moves[j] = moves[j], moves[i] })
	orderCaptures(pos, moves)

	best, alpha := -botInf, -botInf
	var chosen chesslib.Move
	chosen = moves[0]
	for i := range moves {
		v := -negamax(pos.Update(&moves[i]), depth-1, -botInf, -alpha)
		if v > best {
			best, chosen = v, moves[i]
		}
		if v > alpha {
			alpha = v
		}
	}
	return chesslib.UCINotation{}.Encode(nil, &chosen)
}

func negamax(pos *chesslib.Position, depth, alpha, beta int) int {
	moves := pos.ValidMoves()
	if len(moves) == 0 {
		if pos.Status() == chesslib.Checkmate {
			return -botMate - depth // side to move is mated; sooner mate scores harder
		}
		return 0 // stalemate / no legal moves
	}
	if depth == 0 {
		return quiesce(pos, alpha, beta) // settle pending captures before scoring
	}
	orderCaptures(pos, moves)
	best := -botInf
	for i := range moves {
		v := -negamax(pos.Update(&moves[i]), depth-1, -beta, -alpha)
		if v > best {
			best = v
		}
		if v > alpha {
			alpha = v
		}
		if alpha >= beta {
			break // fail-high cutoff
		}
	}
	return best
}

// quiesce is a captures-only search that runs at the main-search horizon so a
// leaf is never scored in the middle of an exchange (the horizon effect). It
// starts from a "stand-pat" (do-nothing) material score — the side to move is
// never forced to keep capturing — then tries each capture, recursing until the
// position is quiet. Bounded because captures strictly reduce material.
func quiesce(pos *chesslib.Position, alpha, beta int) int {
	moves := pos.ValidMoves()
	if len(moves) == 0 {
		if pos.Status() == chesslib.Checkmate {
			return -botMate
		}
		return 0 // stalemate
	}
	stand := evaluate(pos, pos.Turn())
	if stand >= beta {
		return beta
	}
	if stand > alpha {
		alpha = stand
	}
	orderCaptures(pos, moves)
	board := pos.Board()
	for i := range moves {
		if board.Piece(moves[i].S2()) == chesslib.NoPiece {
			continue // quiescence explores captures only
		}
		v := -quiesce(pos.Update(&moves[i]), -beta, -alpha)
		if v >= beta {
			return beta
		}
		if v > alpha {
			alpha = v
		}
	}
	return alpha
}

// evaluate is the leaf heuristic: material plus piece-square positional bonuses,
// own minus opponent, in centipawns from side's perspective. The piece-square
// tables give the bot positional sense — develop pieces toward the centre, push
// central pawns, castle the king into safety — on top of pure material.
func evaluate(pos *chesslib.Position, side chesslib.Color) int {
	pieces := pos.Board().SquareMap()
	phase := gamePhase(pieces)
	score := 0
	for sq, pc := range pieces {
		pt := pc.Type()
		v := matValue[pt]
		if pt == chesslib.King {
			v += kingBonus(sq, pc.Color(), phase) // tapered MG↔EG
		} else {
			v += pstValue(pt, sq, pc.Color())
		}
		if pc.Color() == side {
			score += v
		} else {
			score -= v
		}
	}
	return score
}

// pstIndex maps a square to a table index, oriented so White reads the tables
// (written rank 8 first) directly and Black reads them vertically mirrored — so
// the tables are colour-symmetric.
func pstIndex(sq chesslib.Square, c chesslib.Color) int {
	f, r := int(sq.File()), int(sq.Rank()) // 0..7, rank 1 == 0
	if c == chesslib.White {
		return (7-r)*8 + f
	}
	return r*8 + f
}

// pstValue is the positional bonus for a non-king piece on a square.
func pstValue(pt chesslib.PieceType, sq chesslib.Square, c chesslib.Color) int {
	return pst[pt][pstIndex(sq, c)]
}

// kingBonus tapers the king's positional value between the middlegame table
// (tucked back / castled) and the endgame table (centralised and active) by the
// game phase, so the king comes out to fight as material leaves the board.
func kingBonus(sq chesslib.Square, c chesslib.Color, phase int) int {
	idx := pstIndex(sq, c)
	mg, eg := pst[chesslib.King][idx], kingEG[idx]
	return (mg*phase + eg*(phaseMax-phase)) / phaseMax
}

// gamePhase runs from phaseMax (full material — middlegame) down to 0 (bare
// kings — deep endgame), summed from remaining non-pawn material.
func gamePhase(pieces map[chesslib.Square]chesslib.Piece) int {
	p := 0
	for _, pc := range pieces {
		p += phaseWeight[pc.Type()]
	}
	if p > phaseMax {
		p = phaseMax // guard promotions pushing material above the starting sum
	}
	return p
}

const phaseMax = 24 // starting non-pawn material: 2×(Q·4 + 2R·2 + 2B·1 + 2N·1)

// phaseWeight by chesslib.PieceType (king & pawn contribute 0).
var phaseWeight = [7]int{chesslib.Queen: 4, chesslib.Rook: 2, chesslib.Bishop: 1, chesslib.Knight: 1}

// pst holds the middlegame piece-square tables (Michniewski's simplified set),
// indexed by chesslib.PieceType then square (rank 8 first, files a..h).
var pst = [7][64]int{
	chesslib.Pawn: {
		0, 0, 0, 0, 0, 0, 0, 0,
		50, 50, 50, 50, 50, 50, 50, 50,
		10, 10, 20, 30, 30, 20, 10, 10,
		5, 5, 10, 25, 25, 10, 5, 5,
		0, 0, 0, 20, 20, 0, 0, 0,
		5, -5, -10, 0, 0, -10, -5, 5,
		5, 10, 10, -20, -20, 10, 10, 5,
		0, 0, 0, 0, 0, 0, 0, 0,
	},
	chesslib.Knight: {
		-50, -40, -30, -30, -30, -30, -40, -50,
		-40, -20, 0, 0, 0, 0, -20, -40,
		-30, 0, 10, 15, 15, 10, 0, -30,
		-30, 5, 15, 20, 20, 15, 5, -30,
		-30, 0, 15, 20, 20, 15, 0, -30,
		-30, 5, 10, 15, 15, 10, 5, -30,
		-40, -20, 0, 5, 5, 0, -20, -40,
		-50, -40, -30, -30, -30, -30, -40, -50,
	},
	chesslib.Bishop: {
		-20, -10, -10, -10, -10, -10, -10, -20,
		-10, 0, 0, 0, 0, 0, 0, -10,
		-10, 0, 5, 10, 10, 5, 0, -10,
		-10, 5, 5, 10, 10, 5, 5, -10,
		-10, 0, 10, 10, 10, 10, 0, -10,
		-10, 10, 10, 10, 10, 10, 10, -10,
		-10, 5, 0, 0, 0, 0, 5, -10,
		-20, -10, -10, -10, -10, -10, -10, -20,
	},
	chesslib.Rook: {
		0, 0, 0, 0, 0, 0, 0, 0,
		5, 10, 10, 10, 10, 10, 10, 5,
		-5, 0, 0, 0, 0, 0, 0, -5,
		-5, 0, 0, 0, 0, 0, 0, -5,
		-5, 0, 0, 0, 0, 0, 0, -5,
		-5, 0, 0, 0, 0, 0, 0, -5,
		-5, 0, 0, 0, 0, 0, 0, -5,
		0, 0, 0, 5, 5, 0, 0, 0,
	},
	chesslib.Queen: {
		-20, -10, -10, -5, -5, -10, -10, -20,
		-10, 0, 0, 0, 0, 0, 0, -10,
		-10, 0, 5, 5, 5, 5, 0, -10,
		-5, 0, 5, 5, 5, 5, 0, -5,
		0, 0, 5, 5, 5, 5, 0, -5,
		-10, 5, 5, 5, 5, 5, 0, -10,
		-10, 0, 5, 0, 0, 0, 0, -10,
		-20, -10, -10, -5, -5, -10, -10, -20,
	},
	chesslib.King: { // middlegame: stay tucked away / castled
		-30, -40, -40, -50, -50, -40, -40, -30,
		-30, -40, -40, -50, -50, -40, -40, -30,
		-30, -40, -40, -50, -50, -40, -40, -30,
		-30, -40, -40, -50, -50, -40, -40, -30,
		-20, -30, -30, -40, -40, -30, -30, -20,
		-10, -20, -20, -20, -20, -20, -20, -10,
		20, 20, 0, 0, 0, 0, 20, 20,
		20, 30, 10, 0, 0, 10, 30, 20,
	},
}

// kingEG is the endgame king table: unlike the middlegame king (pst[King]),
// which hides in the corner, the endgame king is a strong piece — centralise it.
var kingEG = [64]int{
	-50, -40, -30, -20, -20, -30, -40, -50,
	-30, -20, -10, 0, 0, -10, -20, -30,
	-30, -10, 20, 30, 30, 20, -10, -30,
	-30, -10, 30, 40, 40, 30, -10, -30,
	-30, -10, 30, 40, 40, 30, -10, -30,
	-30, -10, 20, 30, 30, 20, -10, -30,
	-30, -30, 0, 0, 0, 0, -30, -30,
	-50, -30, -30, -30, -30, -30, -30, -50,
}

// orderCaptures sorts moves so higher-value captures come first (better
// alpha-beta pruning); it is stable, so the prior (shuffled) order is preserved
// among moves with equal capture value.
func orderCaptures(pos *chesslib.Position, moves []chesslib.Move) {
	board := pos.Board()
	sort.SliceStable(moves, func(i, j int) bool {
		return matValue[board.Piece(moves[i].S2()).Type()] > matValue[board.Piece(moves[j].S2()).Type()]
	})
}
