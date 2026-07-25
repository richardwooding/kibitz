package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/xiangqi"
	"github.com/richardwooding/kibitz/internal/session"
)

type xqTable struct {
	client *session.Client
	mux    *service.Mux
	xq     *xiangqi.Service
}

func hostXQ(t *testing.T, url string) (*xqTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	xq := xiangqi.New()
	tb := &xqTable{client: c, mux: service.NewMux(c, xq), xq: xq}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb, phrase
}

func joinXQ(t *testing.T, url, phrase string) *xqTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	xq := xiangqi.New()
	tb := &xqTable{client: c, mux: service.NewMux(c, xq), xq: xq}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func xqWait(t *testing.T, tb *xqTable, match func(xiangqi.State) bool) xiangqi.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.xq.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.xq.State())
	panic("unreachable")
}

func TestXiangqiLateJoinerSyncs(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostXQ(t, url)
	player := joinXQ(t, url, phrase)
	pollStart(t, host.xq.Start)

	xqWait(t, player, func(s xiangqi.State) bool { return s.Playing })
	// Play red's first legal move (from the authoritative legal list).
	first := xqWait(t, host, func(s xiangqi.State) bool { return len(s.Legal) > 0 }).Legal[0]
	if err := host.xq.Move(first[0], first[1]); err != nil {
		t.Fatal(err)
	}
	xqWait(t, player, func(s xiangqi.State) bool { return s.LastFrom == first[0] && s.LastTo == first[1] })
	reply := xqWait(t, player, func(s xiangqi.State) bool { return len(s.Legal) > 0 }).Legal[0]
	if err := player.xq.Move(reply[0], reply[1]); err != nil {
		t.Fatal(err)
	}
	xqWait(t, host, func(s xiangqi.State) bool { return s.LastFrom == reply[0] && s.LastTo == reply[1] })

	late := joinXQ(t, url, phrase)
	st := xqWait(t, late, func(s xiangqi.State) bool { return s.Playing })
	if st.Board != host.xq.State().Board {
		t.Fatalf("late joiner board mismatch")
	}
	hostHist := host.xq.State().History
	lateHist := xqWait(t, late, func(s xiangqi.State) bool { return len(s.History) == len(hostHist) }).History
	for i := range hostHist {
		if lateHist[i] != hostHist[i] {
			t.Fatalf("history[%d] = %q, want %q", i, lateHist[i], hostHist[i])
		}
	}
}
