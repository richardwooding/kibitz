package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/kibitz/internal/session"
)

type c4Table struct {
	client *session.Client
	mux    *service.Mux
	c4     *connect4.Service
}

func hostC4(t *testing.T, url string) (*c4Table, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c4 := connect4.New()
	tb := &c4Table{client: c, mux: service.NewMux(c, c4), c4: c4}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb, phrase
}

func joinC4(t *testing.T, url, phrase string) *c4Table {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c4 := connect4.New()
	tb := &c4Table{client: c, mux: service.NewMux(c, c4), c4: c4}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func c4Wait(t *testing.T, tb *c4Table, match func(connect4.State) bool) connect4.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.c4.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.c4.State())
	panic("unreachable")
}

func TestConnectFourOverRelay(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostC4(t, url)
	player := joinC4(t, url, phrase)
	spectator := joinC4(t, url, phrase)
	pollStart(t, host.c4.Start)

	for _, tb := range []*c4Table{host, player, spectator} {
		st := c4Wait(t, tb, func(s connect4.State) bool { return s.Playing })
		if st.P1ID != host.client.Self() {
			t.Fatalf("P1 = %d, want host", st.P1ID)
		}
	}

	// Host (red) plays column 0 four times; player (yellow) answers in 1.
	tables := map[uint32]*c4Table{
		uint32(host.client.Self()):   host,
		uint32(player.client.Self()): player,
	}
	moves := []struct {
		who uint32
		col int8
	}{
		{uint32(host.client.Self()), 0}, {uint32(player.client.Self()), 1},
		{uint32(host.client.Self()), 0}, {uint32(player.client.Self()), 1},
		{uint32(host.client.Self()), 0}, {uint32(player.client.Self()), 1},
		{uint32(host.client.Self()), 0},
	}
	for _, m := range moves {
		tb := tables[m.who]
		c4Wait(t, tb, func(s connect4.State) bool { return uint32(s.TurnID) == m.who })
		if err := tb.c4.Drop(m.col); err != nil {
			t.Fatalf("drop: %v", err)
		}
	}

	for _, tb := range []*c4Table{host, player, spectator} {
		st := c4Wait(t, tb, func(s connect4.State) bool { return s.Outcome != "" })
		if st.Outcome != "red wins" {
			t.Fatalf("outcome %q", st.Outcome)
		}
		if len(st.WinCells) != 4 {
			t.Fatalf("win cells %v", st.WinCells)
		}
	}
}

func TestConnectFourLateJoinerSyncs(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostC4(t, url)
	player := joinC4(t, url, phrase)
	pollStart(t, host.c4.Start)

	c4Wait(t, player, func(s connect4.State) bool { return s.Playing })
	if err := host.c4.Drop(3); err != nil {
		t.Fatal(err)
	}
	c4Wait(t, player, func(s connect4.State) bool { return s.LastCol == 3 })
	if err := player.c4.Drop(2); err != nil {
		t.Fatal(err)
	}
	c4Wait(t, host, func(s connect4.State) bool { return s.LastCol == 2 })

	late := joinC4(t, url, phrase)
	st := c4Wait(t, late, func(s connect4.State) bool { return s.Playing })
	if st.Board != host.c4.State().Board {
		t.Fatalf("late joiner board mismatch:\nlate: %v\nhost: %v", st.Board, host.c4.State().Board)
	}
	if st.TurnID != host.client.Self() {
		t.Fatalf("late joiner turn = %d", st.TurnID)
	}
}
