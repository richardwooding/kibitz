package solo_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/kibitz/internal/solo"
)

// TestLoopbackConnect4 plays a full Connect Four game across the two loopback
// ends — the real both-sides-validate service on each side — and asserts they
// converge on the same winning state. This exercises the whole solo stack:
// seating handshake, on-demand Start, move broadcast, and hash-verified sync,
// all with no relay.
func TestLoopbackConnect4(t *testing.T) {
	host, guest, seat := solo.New()
	csA := connect4.New()
	muxA := service.NewMux(host, csA)
	csB := connect4.New()
	muxB := service.NewMux(guest, csB)
	// Seating is processed on the host's mux goroutine; wait for the roster to
	// show both members before starting (the bridge gates the UI the same way).
	seated := make(chan struct{})
	go func() {
		closed := false
		for ev := range muxA.Events() {
			if r, ok := ev.(service.Roster); ok && !closed && len(r.Members) >= 2 {
				closed = true
				close(seated)
			}
		}
	}()
	go drain(muxB.Events())
	seat()
	select {
	case <-seated:
	case <-time.After(2 * time.Second):
		t.Fatal("guest was never seated")
	}

	if err := csA.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitConverge(t, csA, csB)

	// Red (host, id 1) stacks column 0; Yellow (guest, id 2) answers in column 1.
	// After four red discs in column 0, red wins vertically.
	for _, col := range []int8{0, 1, 0, 1, 0, 1, 0} {
		st := csA.State()
		if st.Outcome != "" {
			break
		}
		var err error
		switch st.TurnID {
		case host.Self():
			err = csA.Drop(col)
		case guest.Self():
			err = csB.Drop(col)
		default:
			t.Fatalf("no side on turn: %+v", st)
		}
		if err != nil {
			t.Fatalf("drop col %d (turn %d): %v", col, st.TurnID, err)
		}
		waitConverge(t, csA, csB)
	}

	if got := csA.State().Outcome; got != "red wins" {
		t.Fatalf("host outcome = %q, want %q", got, "red wins")
	}
	if got := csB.State().Outcome; got != "red wins" {
		t.Fatalf("guest outcome = %q, want %q", got, "red wins")
	}
}

func drain(ch <-chan any) {
	for range ch {
	}
}

func waitConverge(t *testing.T, a, b *connect4.Service) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reflect.DeepEqual(a.State(), b.State()) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("ends did not converge:\n host=%+v\nguest=%+v", a.State(), b.State())
}
