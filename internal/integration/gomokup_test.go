package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/gomokup"
	"github.com/richardwooding/kibitz/internal/session"
)

type gpTable struct {
	client *session.Client
	mux    *service.Mux
	gp     *gomokup.Service
}

func hostGP(t *testing.T, url string) (*gpTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gp := gomokup.New()
	return &gpTable{client: c, mux: service.NewMux(c, gp), gp: gp}, phrase
}

func joinGP(t *testing.T, url, phrase string) *gpTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gp := gomokup.New()
	return &gpTable{client: c, mux: service.NewMux(c, gp), gp: gp}
}

func gpDrain(tb *gpTable) {
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
}

// gpWaitRoster reads tb's mux events until the roster has n members (so the host
// has keyed all joiners), then returns; the caller starts draining afterward.
func gpWaitRoster(t *testing.T, tb *gpTable, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-tb.mux.Events():
			if !ok {
				t.Fatal("mux closed before roster filled")
			}
			if r, isR := ev.(service.Roster); isR && len(r.Members) >= n {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for roster of %d", n)
		}
	}
}

func gpWait(t *testing.T, tb *gpTable, match func(gomokup.State) bool) gomokup.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.gp.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.gp.State())
	panic("unreachable")
}

// TestGomokuPartyThreeHanded plays a full 3-player Gomoku Party game over the
// relay: turns rotate host→p1→p2, the host completes five-in-a-row, and all
// three ends converge on the same winner with no desync — proving the N-seat
// model + both-sides-validate work for more than two players.
func TestGomokuPartyThreeHanded(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGP(t, url)
	p1 := joinGP(t, url, phrase)
	p2 := joinGP(t, url, phrase)

	// The host must have keyed BOTH joiners before Start (so all three are seated).
	gpWaitRoster(t, host, 3)
	gpDrain(host)
	gpDrain(p1)
	gpDrain(p2)

	pollStart(t, host.gp.Start)

	// Seats are [host, p1, p2]; the host opens. Host builds a five on row 0 while
	// the others fill separate rows (no block, no earlier win).
	type mv struct {
		tb   *gpTable
		r, c int8
	}
	seq := []mv{
		{host, 0, 0}, {p1, 5, 0}, {p2, 10, 0},
		{host, 0, 1}, {p1, 5, 1}, {p2, 10, 1},
		{host, 0, 2}, {p1, 5, 2}, {p2, 10, 2},
		{host, 0, 3}, {p1, 5, 3}, {p2, 10, 3},
		{host, 0, 4}, // host's fifth in a row → win
	}
	for _, m := range seq {
		self := uint32(m.tb.client.Self())
		gpWait(t, m.tb, func(s gomokup.State) bool { return s.Playing && uint32(s.TurnID) == self })
		if err := m.tb.gp.Place(m.r, m.c); err != nil {
			t.Fatalf("place (%d,%d): %v", m.r, m.c, err)
		}
	}

	for _, tb := range []*gpTable{host, p1, p2} {
		st := gpWait(t, tb, func(s gomokup.State) bool { return s.Outcome != "" })
		if uint32(st.WinnerID) != uint32(host.client.Self()) {
			t.Fatalf("winner = %d, want host %d", st.WinnerID, host.client.Self())
		}
		if len(st.WinCells) != 5 {
			t.Fatalf("win cells = %v, want 5", st.WinCells)
		}
		if len(st.Seats) != 3 {
			t.Fatalf("seats = %v, want 3", st.Seats)
		}
	}
}

// TestGomokuPartyLeaveEndsGame: with three seated players, one leaving mid-game
// ends the game as abandoned (no single winner) on every remaining end — the
// v1 rule that avoids racing move/leave ordering across ends.
func TestGomokuPartyLeaveEndsGame(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGP(t, url)
	p1 := joinGP(t, url, phrase)
	p2 := joinGP(t, url, phrase)

	gpWaitRoster(t, host, 3)
	gpDrain(host)
	gpDrain(p1)
	gpDrain(p2)
	pollStart(t, host.gp.Start)

	// One opening move so a game is genuinely in progress.
	gpWait(t, host, func(s gomokup.State) bool { return s.Playing && uint32(s.TurnID) == uint32(host.client.Self()) })
	if err := host.gp.Place(0, 0); err != nil {
		t.Fatalf("place: %v", err)
	}

	// p2 departs → the game ends abandoned for the two who remain.
	if err := p2.client.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tb := range []*gpTable{host, p1} {
		st := gpWait(t, tb, func(s gomokup.State) bool { return s.Outcome != "" })
		if st.Outcome != "abandoned" || st.WinnerID != 0 {
			t.Fatalf("after a leave: outcome=%q winner=%d, want abandoned/0", st.Outcome, st.WinnerID)
		}
	}
}
