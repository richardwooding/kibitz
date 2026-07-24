package bot

import (
	"reflect"
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/backgammon"
	"github.com/richardwooding/kibitz/internal/service/battleship"
	"github.com/richardwooding/kibitz/internal/service/chat"
	"github.com/richardwooding/kibitz/internal/service/checkers"
	"github.com/richardwooding/kibitz/internal/service/chess"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/kibitz/internal/service/reversi"
	"github.com/richardwooding/kibitz/internal/solo"
)

type end struct {
	c4  *connect4.Service
	rv  *reversi.Service
	mux *service.Mux
}

func newEnd(conn service.Conn, level Level) end {
	ch := chat.New()
	cs := chess.New()
	bg := backgammon.New()
	c4 := connect4.New()
	ck := checkers.New()
	rv := reversi.New()
	bs := battleship.New()
	mux := service.NewMux(conn, ch, cs, bg, c4, ck, rv, bs)
	go Drive(mux.Events(), Services{Self: conn.Self(), Chess: cs, BG: bg, C4: c4, CK: ck, RV: rv}, 0, level)
	return end{c4: c4, rv: rv, mux: mux}
}

// TestBotSelfPlay runs the bot on BOTH loopback ends and lets them play full
// games against each other — no relay, no human — proving the bot always picks
// legal moves and the two ends stay in hash-verified sync to a terminal result.
// Both connect4 and reversi fill the board, so they terminate at either level.
func TestBotSelfPlay(t *testing.T) {
	for _, level := range []Level{Easy, Hard} {
		host, guest, seat := solo.New()
		a := newEnd(host, level)
		b := newEnd(guest, level)
		seat()

		for _, g := range []struct {
			name  string
			start func() error
			out   func() string
			agree func() bool
		}{
			{"connect4", a.c4.Start,
				func() string { return a.c4.State().Outcome },
				func() bool { return reflect.DeepEqual(a.c4.State(), b.c4.State()) }},
			{"reversi", a.rv.Start,
				func() string { return a.rv.State().Outcome },
				func() bool { return reflect.DeepEqual(a.rv.State(), b.rv.State()) }},
		} {
			startDeadline := time.Now().Add(2 * time.Second)
			for {
				if err := g.start(); err == nil {
					break
				} else if time.Now().After(startDeadline) {
					t.Fatalf("%s(%d): start never succeeded: %v", g.name, level, err)
				}
				time.Sleep(20 * time.Millisecond)
			}
			deadline := time.Now().Add(10 * time.Second)
			for g.out() == "" && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if g.out() == "" {
				t.Fatalf("%s(%d): bots did not finish a game", g.name, level)
			}
			conv := time.Now().Add(2 * time.Second)
			for !g.agree() && time.Now().Before(conv) {
				time.Sleep(5 * time.Millisecond)
			}
			if !g.agree() {
				t.Fatalf("%s(%d): ends disagree on final state", g.name, level)
			}
		}
	}
}

// TestC4HardWinsAndBlocks: the negamax bot takes an immediate win and blocks the
// opponent's immediate win. Board index = col*Rows + row (row 0 = bottom).
func TestC4HardWinsAndBlocks(t *testing.T) {
	// Bot (disc 1) has three in column 0; it should complete the four.
	var win connect4.Board
	win[0], win[1], win[2] = 1, 1, 1
	if col, ok := c4Hard(win, 1); !ok || col != 0 {
		t.Fatalf("win: got col %d ok %v, want col 0", col, ok)
	}
	// Opponent (disc 2) has three in column 1; the bot (disc 1) must block.
	var block connect4.Board
	block[6], block[7], block[8] = 2, 2, 2
	if col, ok := c4Hard(block, 1); !ok || col != 1 {
		t.Fatalf("block: got col %d ok %v, want col 1", col, ok)
	}
}

// TestCkMaterial checks the material eval from each side's perspective.
func TestCkMaterial(t *testing.T) {
	var b checkers.Board
	b[0], b[1], b[2] = 1, -1, 2 // black man, white man, black king
	if got := ckMaterial(b, checkers.Black); got != 2 {
		t.Fatalf("black material = %d, want 2", got)
	}
	if got := ckMaterial(b, checkers.White); got != -2 {
		t.Fatalf("white material = %d, want -2", got)
	}
}

// TestBgEval: a hit (opponent on the bar) scores higher; an own blot scores lower.
func TestBgEval(t *testing.T) {
	var base backgammon.Board
	var hit backgammon.Board
	hit.Bar[backgammon.Black] = 1 // White sent a Black checker to the bar
	if bgEval(hit, backgammon.White) <= bgEval(base, backgammon.White) {
		t.Fatalf("hit should score higher than base")
	}
	var blot backgammon.Board
	blot.Points[13] = 1 // a lone White checker
	if bgEval(blot, backgammon.White) >= bgEval(base, backgammon.White) {
		t.Fatalf("own blot should score lower than base")
	}
}
