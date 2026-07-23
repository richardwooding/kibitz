package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/checkers"
	"github.com/richardwooding/kibitz/internal/session"
)

type ckTable struct {
	client *session.Client
	mux    *service.Mux
	ck     *checkers.Service
}

func newCKTable(t *testing.T, c *session.Client) *ckTable {
	t.Helper()
	ck := checkers.New()
	tb := &ckTable{client: c, mux: service.NewMux(c, ck), ck: ck}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func ckWait(t *testing.T, tb *ckTable, match func(checkers.State) bool) checkers.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.ck.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.ck.State())
	panic("unreachable")
}

func TestCheckersOverRelay(t *testing.T) {
	url := startRelay(t)
	hc, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	host := newCKTable(t, hc)

	jc, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jc.Close() })
	player := newCKTable(t, jc)

	pollStart(t, host.ck.Start)

	// Host is P1 (dark, moves first) in game 1.
	st := ckWait(t, host, func(s checkers.State) bool { return s.Playing && s.TurnID == host.client.Self() })
	if len(st.Legal) != 7 {
		t.Fatalf("opening legal moves = %d, want 7", len(st.Legal))
	}

	// Play four alternating quiet moves via the legal set.
	tables := []*ckTable{host, player}
	for i := 0; i < 4; i++ {
		tb := tables[i%2]
		self := tb.client.Self()
		st := ckWait(t, tb, func(s checkers.State) bool {
			return s.TurnID == self && len(s.Legal) > 0
		})
		if err := tb.ck.TryMove(st.Legal[0]); err != nil {
			t.Fatalf("move %d: %v", i, err)
		}
	}

	// Everyone converges.
	deadline := time.Now().Add(5 * time.Second)
	for host.ck.State().Board != player.ck.State().Board {
		if time.Now().After(deadline) {
			t.Fatalf("boards diverged")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Late joiner syncs.
	lc, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lc.Close() })
	late := newCKTable(t, lc)
	lst := ckWait(t, late, func(s checkers.State) bool { return s.Playing })
	if lst.Board != host.ck.State().Board {
		t.Fatalf("late joiner board mismatch")
	}
}
