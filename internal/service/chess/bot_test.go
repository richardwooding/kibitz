package chess

import (
	"testing"

	chesslib "github.com/corentings/chess/v2"
)

func posFromFEN(t *testing.T, fen string) *chesslib.Position {
	t.Helper()
	opt, err := chesslib.FEN(fen)
	if err != nil {
		t.Fatalf("bad FEN %q: %v", fen, err)
	}
	return chesslib.NewGame(opt).Position()
}

// TestBotTakesFreeMaterial: an undefended black rook on a2 should be captured.
func TestBotTakesFreeMaterial(t *testing.T) {
	pos := posFromFEN(t, "7k/8/8/8/8/8/r7/R6K w - - 0 1") // white Ra1, black rook a2 (loose)
	if got := bestMaterialMove(pos, botDepth); got != "a1a2" {
		t.Fatalf("bestMaterialMove = %q, want a1a2 (win the rook)", got)
	}
}

// TestBotAvoidsHangingQueen: the d5 pawn is defended by e6, so QxD5 loses the
// queen to the recapture — the minimax must not play it (the old MVV bot would).
func TestBotAvoidsHangingQueen(t *testing.T) {
	pos := posFromFEN(t, "7k/8/4p3/3p4/8/8/3Q4/7K w - - 0 1") // Qd2, black pawns d5 & e6
	if got := bestMaterialMove(pos, botDepth); got == "d2d5" {
		t.Fatalf("bot hung its queen with d2d5")
	}
}

// TestBotFindsMateInOne: back-rank mate is preferred over any material grab.
func TestBotFindsMateInOne(t *testing.T) {
	pos := posFromFEN(t, "6k1/5ppp/8/8/8/8/8/R6K w - - 0 1") // Ra8# (king boxed by its pawns)
	if got := bestMaterialMove(pos, botDepth); got != "a1a8" {
		t.Fatalf("bestMaterialMove = %q, want a1a8 (mate in one)", got)
	}
}

// TestPieceSquare checks the positional tables are oriented and colour-symmetric:
// a central knight beats a rim knight, a pawn is rewarded for advancing, and a
// Black piece mirrors the White value.
func TestPieceSquare(t *testing.T) {
	c3 := chesslib.NewSquare(chesslib.FileC, chesslib.Rank3)
	a3 := chesslib.NewSquare(chesslib.FileA, chesslib.Rank3)
	if pstValue(chesslib.Knight, c3, chesslib.White) <= pstValue(chesslib.Knight, a3, chesslib.White) {
		t.Fatalf("knight: central c3 should beat rim a3")
	}
	e4 := chesslib.NewSquare(chesslib.FileE, chesslib.Rank4)
	e2 := chesslib.NewSquare(chesslib.FileE, chesslib.Rank2)
	if pstValue(chesslib.Pawn, e4, chesslib.White) <= pstValue(chesslib.Pawn, e2, chesslib.White) {
		t.Fatalf("pawn: advanced e4 should beat home e2")
	}
	// Colour symmetry: a Black pawn on e5 mirrors a White pawn on e4.
	e5 := chesslib.NewSquare(chesslib.FileE, chesslib.Rank5)
	if pstValue(chesslib.Pawn, e5, chesslib.Black) != pstValue(chesslib.Pawn, e4, chesslib.White) {
		t.Fatalf("pawn: black e5 should mirror white e4")
	}
}

// TestQuiesceWinsHangingPiece: with an undefended black knight en prise, the
// quiescence search resolves the free capture — the score rises above stand-pat.
func TestQuiesceWinsHangingPiece(t *testing.T) {
	pos := posFromFEN(t, "7k/8/8/4n3/8/8/4R3/7K w - - 0 1") // white Re2, loose black Ne5
	stand := evaluate(pos, pos.Turn())
	q := quiesce(pos, -botInf, botInf)
	if q <= stand {
		t.Fatalf("quiesce = %d, want > stand-pat %d (it should win the knight)", q, stand)
	}
}

// TestQuiesceStandsPatOnBadCapture: QxD6 loses the queen to exd6, so quiescence
// must fall back on the stand-pat score rather than the losing capture.
func TestQuiesceStandsPatOnBadCapture(t *testing.T) {
	pos := posFromFEN(t, "7k/4p3/3p4/8/8/8/3Q4/7K w - - 0 1") // Qd2; d6 pawn defended by e7
	stand := evaluate(pos, pos.Turn())
	if q := quiesce(pos, -botInf, botInf); q != stand {
		t.Fatalf("quiesce = %d, want stand-pat %d (must not enter the losing capture)", q, stand)
	}
}
