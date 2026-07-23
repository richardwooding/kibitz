package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/backgammon"
	"github.com/richardwooding/kibitz/internal/service/chess"
	"github.com/richardwooding/kibitz/internal/session"
)

// fullTable carries every game service for launch-flow tests.
type fullTable struct {
	client *session.Client
	mux    *service.Mux
	chess  *chess.Service
	bg     *backgammon.Service
}

func hostFull(t *testing.T, url string) (*fullTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	cs, bg := chess.New(), backgammon.New()
	tb := &fullTable{client: c, mux: service.NewMux(c, cs, bg), chess: cs, bg: bg}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb, phrase
}

func joinFull(t *testing.T, url, phrase string) *fullTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	cs, bg := chess.New(), backgammon.New()
	tb := &fullTable{client: c, mux: service.NewMux(c, cs, bg), chess: cs, bg: bg}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func TestNothingStartsUnasked(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostFull(t, url)
	joinFull(t, url, phrase)

	// Give the handshake and any (buggy) auto-start time to happen.
	time.Sleep(700 * time.Millisecond)
	if host.chess.State().Playing {
		t.Fatal("chess started without Start()")
	}
	if host.bg.State().Playing {
		t.Fatal("backgammon started without Start()")
	}
}

func TestPlayerInitiatedStart(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostFull(t, url)
	player := joinFull(t, url, phrase)

	// The PLAYER starts chess: startReq → host seats and broadcasts.
	if err := player.chess.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !host.chess.State().Playing || !player.chess.State().Playing {
		if time.Now().After(deadline) {
			t.Fatalf("player-initiated start never landed (host playing=%v player playing=%v)",
				host.chess.State().Playing, player.chess.State().Playing)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Backgammon must still be idle — starts are per-game.
	if host.bg.State().Playing {
		t.Fatal("starting chess also started backgammon")
	}
}

func TestRematchSwapsSeats(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostFull(t, url)
	player := joinFull(t, url, phrase)

	pollStart(t, host.chess.Start)
	waitChessPlaying := func(tb *fullTable) chess.State {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if st := tb.chess.State(); st.Playing && st.Outcome == "*" {
				return st
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("chess never (re)started: %+v", tb.chess.State())
		panic("unreachable")
	}

	st := waitChessPlaying(player)
	if st.WhiteID != host.client.Self() {
		t.Fatalf("game 1 white = %d, want host", st.WhiteID)
	}

	// Finish game 1 (player resigns) and rematch from the player side.
	deadline := time.Now().Add(10 * time.Second)
	for player.chess.Resign() != nil {
		if time.Now().After(deadline) {
			t.Fatal("resign never succeeded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The host must see game 1 END before the rematch request, or the
	// startReq is (correctly) rejected as in-progress.
	for host.chess.State().Outcome == "*" {
		if time.Now().After(deadline) {
			t.Fatal("host never saw game 1 end")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pollStart(t, player.chess.Start)

	st2 := waitChessPlaying(host)
	if st2.WhiteID != player.client.Self() {
		t.Fatalf("game 2 white = %d, want player (seats must swap)", st2.WhiteID)
	}
}

func TestTwoGamesConcurrently(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostFull(t, url)
	player := joinFull(t, url, phrase)

	pollStart(t, host.chess.Start)
	pollStart(t, host.bg.Start)

	deadline := time.Now().Add(10 * time.Second)
	for {
		cs, bs := player.chess.State(), player.bg.State()
		if cs.Playing && bs.Playing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("concurrent games: chess=%v bg=%v", cs.Playing, bs.Playing)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A chess move and a backgammon roll both work in the same session.
	deadline = time.Now().Add(10 * time.Second)
	for host.chess.TryMove("e2e4") != nil {
		if time.Now().After(deadline) {
			t.Fatal("chess move never succeeded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Backgammon is mid-opening or moving; just confirm it reaches moving.
	for player.bg.State().Phase != "moving" && player.bg.State().Phase != "rolling" {
		if time.Now().After(deadline) {
			t.Fatalf("backgammon stuck in %q", player.bg.State().Phase)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
