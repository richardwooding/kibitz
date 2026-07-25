package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/hex"
	"github.com/richardwooding/kibitz/internal/session"
)

type hexTable struct {
	client *session.Client
	mux    *service.Mux
	hx     *hex.Service
}

func hostHex(t *testing.T, url string) (*hexTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	hx := hex.New()
	tb := &hexTable{client: c, mux: service.NewMux(c, hx), hx: hx}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb, phrase
}

func joinHex(t *testing.T, url, phrase string) *hexTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	hx := hex.New()
	tb := &hexTable{client: c, mux: service.NewMux(c, hx), hx: hx}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func hexWait(t *testing.T, tb *hexTable, match func(hex.State) bool) hex.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.hx.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.hx.State())
	panic("unreachable")
}

func TestHexLateJoinerSyncs(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostHex(t, url)
	player := joinHex(t, url, phrase)
	pollStart(t, host.hx.Start)

	hexWait(t, player, func(s hex.State) bool { return s.Playing })
	if err := host.hx.Place(5, 5); err != nil { // red f6
		t.Fatal(err)
	}
	hexWait(t, player, func(s hex.State) bool { return s.Last == 5*hex.N+5 })
	if err := player.hx.Place(3, 3); err != nil { // blue
		t.Fatal(err)
	}
	hexWait(t, host, func(s hex.State) bool { return s.Last == 3*hex.N+3 })

	late := joinHex(t, url, phrase)
	st := hexWait(t, late, func(s hex.State) bool { return s.Playing })
	if st.Board != host.hx.State().Board {
		t.Fatalf("late joiner board mismatch")
	}
	hostHist := host.hx.State().History
	lateHist := hexWait(t, late, func(s hex.State) bool { return len(s.History) == len(hostHist) }).History
	for i := range hostHist {
		if lateHist[i] != hostHist[i] {
			t.Fatalf("history[%d] = %q, want %q", i, lateHist[i], hostHist[i])
		}
	}
}
