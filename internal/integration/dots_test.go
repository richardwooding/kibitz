package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/dots"
	"github.com/richardwooding/parley/session"
)

type dotsTable struct {
	client *session.Client
	mux    *service.Mux
	dt     *dots.Service
}

func hostDots(t *testing.T, url string) (*dotsTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	dt := dots.New()
	tb := &dotsTable{client: c, mux: service.NewMux(c, dt), dt: dt}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb, phrase
}

func joinDots(t *testing.T, url, phrase string) *dotsTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	dt := dots.New()
	tb := &dotsTable{client: c, mux: service.NewMux(c, dt), dt: dt}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func dotsWait(t *testing.T, tb *dotsTable, match func(dots.State) bool) dots.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.dt.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.dt.State())
	panic("unreachable")
}

func TestDotsLateJoinerSyncs(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostDots(t, url)
	player := joinDots(t, url, phrase)
	pollStart(t, host.dt.Start)

	dotsWait(t, player, func(s dots.State) bool { return s.Playing })
	if err := host.dt.DrawEdge(0); err != nil {
		t.Fatal(err)
	}
	dotsWait(t, player, func(s dots.State) bool { return s.Last == 0 })
	if err := player.dt.DrawEdge(31); err != nil {
		t.Fatal(err)
	}
	dotsWait(t, host, func(s dots.State) bool { return s.Last == 31 })

	late := joinDots(t, url, phrase)
	st := dotsWait(t, late, func(s dots.State) bool { return s.Playing })
	if st.Edges != host.dt.State().Edges || st.Boxes != host.dt.State().Boxes {
		t.Fatalf("late joiner board mismatch")
	}
	hostHist := host.dt.State().History
	lateHist := dotsWait(t, late, func(s dots.State) bool { return len(s.History) == len(hostHist) }).History
	for i := range hostHist {
		if lateHist[i] != hostHist[i] {
			t.Fatalf("history[%d] = %q, want %q", i, lateHist[i], hostHist[i])
		}
	}
}
