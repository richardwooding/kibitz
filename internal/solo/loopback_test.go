package solo_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/solo"
)

func recvFrame(t *testing.T, e *solo.Endpoint) session.Frame {
	t.Helper()
	select {
	case ev := <-e.Events():
		fr, ok := ev.(session.Frame)
		if !ok {
			t.Fatalf("want Frame, got %T", ev)
		}
		return fr
	case <-time.After(time.Second):
		t.Fatal("no frame delivered")
		panic("unreachable")
	}
}

// TestPartyBroadcastAndRouting: an N-end party fans a Broadcast to every other
// end (same seq) and routes SendTo to exactly the addressed end; seat() emits
// one MemberKeyed per guest to the host.
func TestPartyBroadcastAndRouting(t *testing.T) {
	host, guests, seat := solo.NewParty(2)
	if len(guests) != 2 {
		t.Fatalf("guests = %d, want 2", len(guests))
	}

	seat()
	for i := 0; i < 2; i++ {
		if _, ok := (<-host.Events()).(session.MemberKeyed); !ok {
			t.Fatal("seat() should emit a MemberKeyed per guest")
		}
	}

	// Host broadcast reaches both guests with the same sequence.
	if err := host.Broadcast("svc", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for i, g := range guests {
		fr := recvFrame(t, g)
		if fr.From != host.Self() || string(fr.Envelope.Body) != "hi" {
			t.Fatalf("guest %d got %+v", i, fr)
		}
		if i == 0 {
			seq = fr.Envelope.Seq
		} else if fr.Envelope.Seq != seq {
			t.Fatalf("broadcast seq differs across recipients: %d vs %d", fr.Envelope.Seq, seq)
		}
	}

	// A guest's SendTo(host) reaches only the host; the other guest sees nothing.
	if err := guests[0].SendTo(host.Self(), "svc", []byte("yo")); err != nil {
		t.Fatal(err)
	}
	if fr := recvFrame(t, host); fr.From != guests[0].Self() || string(fr.Envelope.Body) != "yo" {
		t.Fatalf("host got %+v, want a Direct from guest 0", fr)
	}
	select {
	case ev := <-guests[1].Events():
		t.Fatalf("guest 1 received a Direct not addressed to it: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

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
		// CanTakeback is a per-end view (only the last mover may offer), so it
		// legitimately differs between the two ends — normalise it out of the
		// shared-state convergence check.
		sa, sb := a.State(), b.State()
		sa.CanTakeback, sb.CanTakeback = false, false
		if reflect.DeepEqual(sa, sb) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("ends did not converge:\n host=%+v\nguest=%+v", a.State(), b.State())
}
