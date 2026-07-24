// Package bot is the solo "Play the computer" opponent. It drives one end of the
// in-memory solo loopback: it watches that end's merged event stream and, on
// each game state where it is the side to move, plays a legal move on that end's
// service. There is no game-specific AI engine — it reads the legal moves the
// services already expose (or derives them) and picks one. This is the single
// seam where per-game difficulty can grow; the MVP plays a uniformly random
// legal move (easy and welcoming for a first try).
package bot

import (
	"math/rand"
	"time"

	"github.com/richardwooding/kibitz/internal/service/backgammon"
	"github.com/richardwooding/kibitz/internal/service/checkers"
	"github.com/richardwooding/kibitz/internal/service/chess"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/kibitz/internal/service/reversi"
	"github.com/richardwooding/kibitz/internal/wire"
)

// Services is the one end the bot plays on: its game-service set plus the bot's
// own participant id (the side it moves for).
type Services struct {
	Self  wire.ParticipantID
	Chess *chess.Service
	BG    *backgammon.Service
	C4    *connect4.Service
	CK    *checkers.Service
	RV    *reversi.Service
}

// Drive consumes an end's merged event stream and plays the bot's move whenever
// that end is the side to move. It returns when the stream closes, so run it in
// a goroutine (it also drains the stream). delay paces each move for a natural
// feel (pass 0 in tests). Move methods are turn-checked by the services, so a
// stray call is simply rejected.
func Drive(events <-chan any, s Services, delay time.Duration) {
	pause := func() {
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	for ev := range events {
		switch e := ev.(type) {
		case connect4.State:
			if e.Playing && e.Outcome == "" && e.TurnID == s.Self {
				if col, ok := c4Pick(e.Board); ok {
					pause()
					_ = s.C4.Drop(col)
				}
			}
		case reversi.State:
			if e.Playing && e.Outcome == "" && e.TurnID == s.Self && len(e.Legal) > 0 {
				pause()
				_ = s.RV.PlaceDisc(e.Legal[rand.Intn(len(e.Legal))])
			}
		case checkers.State:
			if e.Playing && e.Outcome == "" && e.TurnID == s.Self && len(e.Legal) > 0 {
				pause()
				_ = s.CK.TryMove([]int8(e.Legal[rand.Intn(len(e.Legal))]))
			}
		case chess.State:
			// Chess uses "*" for an in-progress game.
			if e.Playing && e.Outcome == "*" && e.TurnID == s.Self {
				if mv := s.Chess.LegalMoves(); len(mv) > 0 {
					pause()
					_ = s.Chess.TryMove(mv[rand.Intn(len(mv))])
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
						pause()
						_ = s.BG.Move(e.Legal[rand.Intn(len(e.Legal))])
					}
				}
			}
		}
	}
}

// c4Pick returns a random column with room (top cell empty), false if the board
// is full. Connect Four has no legal-move list, so derive it from the board.
func c4Pick(b connect4.Board) (int8, bool) {
	var legal []int8
	for c := 0; c < connect4.Cols; c++ {
		if b[c*connect4.Rows+(connect4.Rows-1)] == 0 {
			legal = append(legal, int8(c))
		}
	}
	if len(legal) == 0 {
		return 0, false
	}
	return legal[rand.Intn(len(legal))], true
}
