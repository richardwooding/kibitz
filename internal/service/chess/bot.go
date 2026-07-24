package chess

import (
	"math/rand"
	"sort"

	chesslib "github.com/corentings/chess/v2"
)

// The solo "Play the computer" bot's Hard move for chess: a small alpha-beta
// negamax over material. It searches a CLONE of the position (corentings/chess
// positions are immutable — Update returns a new one), so it never touches the
// live game. Material-only eval, captures searched first for pruning; it finds
// short tactics and, crucially, won't hang a piece to a recapture the way the
// old static heuristic could.

const (
	botDepth  = 4       // plies of lookahead
	botInf    = 1 << 30 // search infinity (> any score)
	botMate   = 1 << 20 // checkmate magnitude (≫ any material total)
	botMaxMat = 1 << 16 // guard: material never approaches mate/inf
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
	stand := evalMaterial(pos, pos.Turn())
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

// evalMaterial is the leaf heuristic: own material minus opponent's, in
// centipawns, from side's perspective.
func evalMaterial(pos *chesslib.Position, side chesslib.Color) int {
	score := 0
	for _, pc := range pos.Board().SquareMap() {
		v := matValue[pc.Type()]
		if pc.Color() == side {
			score += v
		} else {
			score -= v
		}
	}
	return score
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
