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
