package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/reversi"
	"github.com/richardwooding/kibitz/internal/session"
)

type rvTable struct {
	client *session.Client
	mux    *service.Mux
	rv     *reversi.Service
}

func newRVTable(t *testing.T, c *session.Client) *rvTable {
	t.Helper()
	rv := reversi.New()
	tb := &rvTable{client: c, mux: service.NewMux(c, rv), rv: rv}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func rvWait(t *testing.T, tb *rvTable, match func(reversi.State) bool) reversi.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.rv.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.rv.State())
	panic("unreachable")
}

// TestReversiFullGameOverRelay plays a complete game (first-legal-square
// strategy) through the relay — exercising computed passes if they occur —
// and checks both ends agree on the final score.
func TestReversiFullGameOverRelay(t *testing.T) {
	url := startRelay(t)
	hc, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	host := newRVTable(t, hc)

	jc, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jc.Close() })
	player := newRVTable(t, jc)

	pollStart(t, host.rv.Start)
	rvWait(t, player, func(s reversi.State) bool { return s.Playing })

	tables := []*rvTable{host, player}
	deadline := time.Now().Add(60 * time.Second)
	for host.rv.State().Outcome == "" {
		if time.Now().After(deadline) {
			t.Fatalf("game never finished (host: %+v)", host.rv.State())
		}
		moved := false
		for _, tb := range tables {
			st := tb.rv.State()
			if st.Playing && st.Outcome == "" && st.TurnID == tb.client.Self() && len(st.Legal) > 0 {
				if err := tb.rv.PlaceDisc(st.Legal[0]); err != nil {
					t.Fatalf("place: %v", err)
				}
				moved = true
				break
			}
		}
		if !moved {
			time.Sleep(20 * time.Millisecond)
		}
	}

	hostFinal := rvWait(t, host, func(s reversi.State) bool { return s.Outcome != "" })
	playerFinal := rvWait(t, player, func(s reversi.State) bool { return s.Outcome != "" })
	if hostFinal.Outcome != playerFinal.Outcome {
		t.Fatalf("outcomes differ: %q vs %q", hostFinal.Outcome, playerFinal.Outcome)
	}
	if hostFinal.Black+hostFinal.White < 20 {
		t.Fatalf("suspiciously short game: %d discs", hostFinal.Black+hostFinal.White)
	}
	if hostFinal.Board != playerFinal.Board {
		t.Fatal("final boards differ")
	}
}
