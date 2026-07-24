package bot_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/bot"
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

// newEnd builds one loopback end's full service set + mux, with the bot driving
// it. (Both ends are bot-driven in the test, so they play each other.)
func newEnd(conn service.Conn) end {
	ch := chat.New()
	cs := chess.New()
	bg := backgammon.New()
	c4 := connect4.New()
	ck := checkers.New()
	rv := reversi.New()
	bs := battleship.New()
	mux := service.NewMux(conn, ch, cs, bg, c4, ck, rv, bs)
	go bot.Drive(mux.Events(), bot.Services{
		Self: conn.Self(), Chess: cs, BG: bg, C4: c4, CK: ck, RV: rv,
	}, 0)
	return end{c4: c4, rv: rv, mux: mux}
}

// TestBotSelfPlay runs the bot on BOTH loopback ends and lets them play full
// games against each other — no relay, no human — proving the bot always picks
// legal moves and the two ends stay in hash-verified sync to a terminal result.
// Connect Four and reversi both fill the board, so they terminate.
func TestBotSelfPlay(t *testing.T) {
	host, guest, seat := solo.New()
	a := newEnd(host)
	b := newEnd(guest)
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
		// Retry Start until the guest is seated (seating is async on the host mux).
		startDeadline := time.Now().Add(2 * time.Second)
		var err error
		for {
			if err = g.start(); err == nil {
				break
			}
			if time.Now().After(startDeadline) {
				t.Fatalf("%s: start never succeeded: %v", g.name, err)
			}
			time.Sleep(20 * time.Millisecond)
		}

		deadline := time.Now().Add(10 * time.Second)
		for g.out() == "" && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if g.out() == "" {
			t.Fatalf("%s: bots did not finish a game", g.name)
		}
		conv := time.Now().Add(2 * time.Second)
		for !g.agree() && time.Now().Before(conv) {
			time.Sleep(5 * time.Millisecond)
		}
		if !g.agree() {
			t.Fatalf("%s: ends disagree on final state", g.name)
		}
		t.Logf("%s finished: %q", g.name, g.out())
	}
}
