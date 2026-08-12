package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/gomoku"
	"github.com/richardwooding/parley/session"
)

type gmTable struct {
	client *session.Client
	mux    *service.Mux
	gm     *gomoku.Service
}

func hostGM(t *testing.T, url string) (*gmTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url, session.WithProtocol(proto.Label))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gm := gomoku.New()
	tb := &gmTable{client: c, mux: service.NewMux(c, gm), gm: gm}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb, phrase
}

func joinGM(t *testing.T, url, phrase string) *gmTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, false, session.WithProtocol(proto.Label))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gm := gomoku.New()
	tb := &gmTable{client: c, mux: service.NewMux(c, gm), gm: gm}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func gmWait(t *testing.T, tb *gmTable, match func(gomoku.State) bool) gomoku.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.gm.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.gm.State())
	panic("unreachable")
}

func TestGomokuOverRelay(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGM(t, url)
	player := joinGM(t, url, phrase)
	spectator := joinGM(t, url, phrase)
	pollStart(t, host.gm.Start)

	for _, tb := range []*gmTable{host, player, spectator} {
		st := gmWait(t, tb, func(s gomoku.State) bool { return s.Playing })
		if st.P1ID != host.client.Self() {
			t.Fatalf("P1 = %d, want host", st.P1ID)
		}
	}

	// Black (host) builds a horizontal five on row 7; white (player) answers on row 0.
	tables := map[uint32]*gmTable{
		uint32(host.client.Self()):   host,
		uint32(player.client.Self()): player,
	}
	type mv struct {
		who      uint32
		row, col int8
	}
	moves := []mv{
		{uint32(host.client.Self()), 7, 3}, {uint32(player.client.Self()), 0, 0},
		{uint32(host.client.Self()), 7, 4}, {uint32(player.client.Self()), 0, 1},
		{uint32(host.client.Self()), 7, 5}, {uint32(player.client.Self()), 0, 2},
		{uint32(host.client.Self()), 7, 6}, {uint32(player.client.Self()), 0, 3},
		{uint32(host.client.Self()), 7, 7},
	}
	for _, m := range moves {
		tb := tables[m.who]
		gmWait(t, tb, func(s gomoku.State) bool { return uint32(s.TurnID) == m.who })
		if err := tb.gm.Place(m.row, m.col); err != nil {
			t.Fatalf("place (%d,%d): %v", m.row, m.col, err)
		}
	}

	for _, tb := range []*gmTable{host, player, spectator} {
		st := gmWait(t, tb, func(s gomoku.State) bool { return s.Outcome != "" })
		if st.Outcome != "black wins" {
			t.Fatalf("outcome %q", st.Outcome)
		}
		if len(st.WinCells) != 5 {
			t.Fatalf("win cells %v", st.WinCells)
		}
	}
}

func TestGomokuLateJoinerSyncs(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGM(t, url)
	player := joinGM(t, url, phrase)
	pollStart(t, host.gm.Start)

	gmWait(t, player, func(s gomoku.State) bool { return s.Playing })
	if err := host.gm.Place(7, 7); err != nil { // black h8
		t.Fatal(err)
	}
	gmWait(t, player, func(s gomoku.State) bool { return s.Last == 7*gomoku.Size+7 })
	if err := player.gm.Place(7, 8); err != nil { // white i8
		t.Fatal(err)
	}
	gmWait(t, host, func(s gomoku.State) bool { return s.Last == 7*gomoku.Size+8 })

	late := joinGM(t, url, phrase)
	st := gmWait(t, late, func(s gomoku.State) bool { return s.Playing })
	if st.Board != host.gm.State().Board {
		t.Fatalf("late joiner board mismatch")
	}
	if st.TurnID != host.client.Self() {
		t.Fatalf("late joiner turn = %d", st.TurnID)
	}
	// History is authoritative in state + snapshot: the late joiner receives the
	// full list via the snapshot, not just moves made after it joined.
	hostHist := host.gm.State().History
	if len(hostHist) != 2 || hostHist[0] != "⚫ h8" || hostHist[1] != "⚪ i8" {
		t.Fatalf("host history = %v, want [⚫ h8 ⚪ i8]", hostHist)
	}
	lateHist := gmWait(t, late, func(s gomoku.State) bool { return len(s.History) == 2 }).History
	if lateHist[0] != hostHist[0] || lateHist[1] != hostHist[1] {
		t.Fatalf("late joiner history = %v, want %v", lateHist, hostHist)
	}
}
