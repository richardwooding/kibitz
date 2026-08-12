package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/weiqi"
	"github.com/richardwooding/parley/session"
)

type weiqiTable struct {
	client *session.Client
	mux    *service.Mux
	wq     *weiqi.Service
}

func hostWeiqi(t *testing.T, url string) (*weiqiTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url, session.WithProtocol(proto.Label))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	wq := weiqi.New()
	tb := &weiqiTable{client: c, mux: service.NewMux(c, wq), wq: wq}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb, phrase
}

func joinWeiqi(t *testing.T, url, phrase string) *weiqiTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, false, session.WithProtocol(proto.Label))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	wq := weiqi.New()
	tb := &weiqiTable{client: c, mux: service.NewMux(c, wq), wq: wq}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func weiqiWait(t *testing.T, tb *weiqiTable, match func(weiqi.State) bool) weiqi.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.wq.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.wq.State())
	panic("unreachable")
}

func TestWeiqiLateJoinerSyncs(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostWeiqi(t, url)
	player := joinWeiqi(t, url, phrase)
	pollStart(t, host.wq.Start)

	weiqiWait(t, player, func(s weiqi.State) bool { return s.Playing })
	if err := host.wq.Place(2, 2); err != nil { // black
		t.Fatal(err)
	}
	weiqiWait(t, player, func(s weiqi.State) bool { return s.Last == 2*weiqi.N+2 })
	if err := player.wq.Place(6, 6); err != nil { // white
		t.Fatal(err)
	}
	weiqiWait(t, host, func(s weiqi.State) bool { return s.Last == 6*weiqi.N+6 })

	late := joinWeiqi(t, url, phrase)
	st := weiqiWait(t, late, func(s weiqi.State) bool { return s.Playing })
	if st.Board != host.wq.State().Board {
		t.Fatalf("late joiner board mismatch")
	}
	hostHist := host.wq.State().History
	lateHist := weiqiWait(t, late, func(s weiqi.State) bool { return len(s.History) == len(hostHist) }).History
	for i := range hostHist {
		if lateHist[i] != hostHist[i] {
			t.Fatalf("history[%d] = %q, want %q", i, lateHist[i], hostHist[i])
		}
	}
}
