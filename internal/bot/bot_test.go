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
	"github.com/richardwooding/kibitz/internal/service/gomokup"
	"github.com/richardwooding/kibitz/internal/service/reversi"
	"github.com/richardwooding/kibitz/internal/solo"
)

func waitUntil(t *testing.T, d time.Duration, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition %q not met within %s", what, d)
}

func gpStones(b gomokup.Board) int {
	n := 0
	for _, v := range b {
		if v != 0 {
			n++
		}
	}
	return n
}

// TestGpPickCompletesAndBlocks: the Hard heuristic completes its own open four
// and, lacking one, blocks an opponent's open four — the decisive moves.
func TestGpPickCompletesAndBlocks(t *testing.T) {
	var mine gomokup.Board
	for c := 5; c <= 8; c++ {
		mine[10*gomokup.Size+c] = 1 // my four, cols 5–8 on row 10
	}
	r, c, ok := gpPick(Hard, mine, 1, 3)
	if !ok || int(r) != 10 || (int(c) != 4 && int(c) != 9) {
		t.Fatalf("complete four = (%d,%d),%v; want row 10 col 4 or 9", r, c, ok)
	}

	var theirs gomokup.Board
	for c := 5; c <= 8; c++ {
		theirs[3*gomokup.Size+c] = 2 // opponent's four, row 3
	}
	r, c, ok = gpPick(Hard, theirs, 1, 3) // I'm color 1 with nothing → block color 2
	if !ok || int(r) != 3 || (int(c) != 4 && int(c) != 9) {
		t.Fatalf("block four = (%d,%d),%v; want row 3 col 4 or 9", r, c, ok)
	}
}

// TestGomokupBotSelfPlay: a host end plus two bot ends over the N-way loopback
// play a full 3-handed Gomoku Party — bots take seats in the lobby, the host
// begins, and all three ends converge on the same terminal board with no desync.
func TestGomokupBotSelfPlay(t *testing.T) {
	host, guests, seat := solo.NewParty(2)
	hostGP := gomokup.New()
	hostMux := service.NewMux(host, chat.New(), hostGP)
	go Drive(hostMux.Events(), Services{Self: host.Self(), GP: hostGP}, time.Millisecond, Hard)
	gps := []*gomokup.Service{hostGP}
	for _, g := range guests {
		gp := gomokup.New()
		m := service.NewMux(g, chat.New(), gp)
		go Drive(m.Events(), Services{Self: g.Self(), GP: gp}, time.Millisecond, Hard)
		gps = append(gps, gp)
	}
	seat()

	if err := hostGP.Start(); err != nil { // open the lobby → bots take seats
		t.Fatal(err)
	}
	waitUntil(t, 3*time.Second, "three seated", func() bool { return len(hostGP.State().Seats) >= 3 })
	if err := hostGP.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	waitUntil(t, 25*time.Second, "game over", func() bool { return hostGP.State().Outcome != "" })
	waitUntil(t, 3*time.Second, "ends converge", func() bool {
		a := hostGP.State()
		for _, gp := range gps[1:] {
			b := gp.State()
			if a.Outcome != b.Outcome || a.Board != b.Board {
				return false
			}
		}
		return true
	})
	if gpStones(hostGP.State().Board) < 5 {
		t.Fatalf("suspiciously short game: %d stones", gpStones(hostGP.State().Board))
	}
}

type end struct {
	c4  *connect4.Service
	rv  *reversi.Service
	bs  *battleship.Service
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
	// A small move delay reflects real pacing (the app uses 500ms). It also keeps
	// battleship self-play honest: a shot's reveal is a round-trip, so two bots
	// reacting with zero delay can fire the next shot before the reveal lands.
	go Drive(mux.Events(), Services{Self: conn.Self(), Chess: cs, BG: bg, C4: c4, CK: ck, RV: rv, BS: bs}, 3*time.Millisecond, level)
	return end{c4: c4, rv: rv, bs: bs, mux: mux}
}

// TestBotSelfPlay runs the bot on BOTH loopback ends and lets them play full
// games against each other — no relay, no human — proving the bot always picks
// legal moves and the two ends stay in hash-verified sync to a terminal result.
// Both connect4 and reversi fill the board, so they terminate at either level.
func TestBotSelfPlay(t *testing.T) {
	for _, level := range []Level{Easy, Medium, Hard} {
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

// TestResolveLevel: Medium slips to Easy below the mistake threshold and plays
// Hard above it; Easy and Hard are unaffected by the draw.
func TestResolveLevel(t *testing.T) {
	if got := resolveLevel(Medium, mediumMistake/2); got != Easy {
		t.Fatalf("Medium low-r = %v, want Easy", got)
	}
	if got := resolveLevel(Medium, (mediumMistake+1)/2); got != Hard {
		t.Fatalf("Medium high-r = %v, want Hard", got)
	}
	if resolveLevel(Easy, 0.99) != Easy || resolveLevel(Hard, 0.01) != Hard {
		t.Fatal("Easy/Hard must ignore the draw")
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

// TestBsTarget: Easy fires anywhere un-shot; Hard targets an un-shot neighbour of
// a live (unsunk) hit, and hunts on parity when there are no live hits.
func TestBsTarget(t *testing.T) {
	var blank [100]int8
	for i := range blank {
		blank[i] = -1
	}
	if _, ok := bsTarget(blank, nil, Easy); !ok {
		t.Fatal("easy should find an un-shot cell on a blank board")
	}
	// One live hit at cell 44 (ship 1, not sunk): Hard must shoot a neighbour.
	hit := blank
	hit[44] = 1
	nbrs := map[uint8]bool{34: true, 54: true, 43: true, 45: true}
	got, ok := bsTarget(hit, nil, Hard)
	if !ok || !nbrs[got] {
		t.Fatalf("hard target = %d ok=%v, want a neighbour of 44 %v", got, ok, nbrs)
	}
	// Same hit but ship 1 already sunk → no live hit → hunt on parity.
	if got, ok := bsTarget(hit, []uint8{1}, Hard); !ok || (int(got)%10+int(got)/10)%2 != 0 {
		t.Fatalf("hard hunt = %d ok=%v, want a parity cell", got, ok)
	}
}

// TestBotBattleshipSelfPlay: two bot ends play a full Battleship game over the
// loopback — both place random fleets, then shoot to a finish — proving the
// placement→shooting→validating flow works end to end with the real service.
func TestBotBattleshipSelfPlay(t *testing.T) {
	host, guest, seat := solo.New()
	a := newEnd(host, Hard)
	_ = newEnd(guest, Hard)
	seat()

	startDeadline := time.Now().Add(2 * time.Second)
	for {
		if err := a.bs.Start(); err == nil {
			break
		} else if time.Now().After(startDeadline) {
			t.Fatalf("battleship start never succeeded: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline := time.Now().Add(15 * time.Second)
	for a.bs.State().Phase != "over" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := a.bs.State().Phase; got != "over" {
		t.Fatalf("battleship did not finish: phase=%q outcome=%q", got, a.bs.State().Outcome)
	}
}
