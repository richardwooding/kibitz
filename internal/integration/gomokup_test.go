package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/gomokup"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
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

func seatsHave(st gomokup.State, id wire.ParticipantID) bool {
	for _, s := range st.Seats {
		if s == id {
			return true
		}
	}
	return false
}

// openAndSeat opens the lobby (host), has each joiner take a seat, and waits for
// every end to see all three seated. Returns once the lobby holds host+p1+p2.
func openAndSeat(t *testing.T, host, p1, p2 *gpTable) {
	t.Helper()
	if err := host.gp.Start(); err != nil { // opens the lobby (host auto-seats)
		t.Fatalf("open: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return s.Lobby })
	}
	if err := p1.gp.TakeSeat(); err != nil {
		t.Fatalf("p1 take seat: %v", err)
	}
	if err := p2.gp.TakeSeat(); err != nil {
		t.Fatalf("p2 take seat: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool {
			return s.Lobby && len(s.Seats) == 3 &&
				seatsHave(s, host.client.Self()) && seatsHave(s, p1.client.Self()) && seatsHave(s, p2.client.Self())
		})
	}
}

// TestGomokuPartyLobbyThreeHanded exercises the full lobby → play path: the host
// opens a table, two joiners take seats (host auto-seated), the host begins, and
// a 3-player game plays to a five-in-a-row win with all ends converged.
func TestGomokuPartyLobbyThreeHanded(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGP(t, url)
	p1 := joinGP(t, url, phrase)
	p2 := joinGP(t, url, phrase)
	gpWaitRoster(t, host, 3)
	gpDrain(host)
	gpDrain(p1)
	gpDrain(p2)

	openAndSeat(t, host, p1, p2)

	if err := host.gp.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return s.Playing && len(s.Seats) == 3 })
	}

	// Seats are [host, p1, p2]; host opens. Host builds a five on row 0 while the
	// others fill separate rows.
	type mv struct {
		tb   *gpTable
		r, c int8
	}
	seq := []mv{
		{host, 0, 0}, {p1, 5, 0}, {p2, 10, 0},
		{host, 0, 1}, {p1, 5, 1}, {p2, 10, 1},
		{host, 0, 2}, {p1, 5, 2}, {p2, 10, 2},
		{host, 0, 3}, {p1, 5, 3}, {p2, 10, 3},
		{host, 0, 4}, // host's fifth → win
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
	}
}

// TestGomokuPartyLobbyLeaveAndGate: a claimant can release a seat (freeing it on
// every end), and the host cannot begin with fewer than two seated.
func TestGomokuPartyLobbyLeaveAndGate(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGP(t, url)
	p1 := joinGP(t, url, phrase)
	p2 := joinGP(t, url, phrase)
	gpWaitRoster(t, host, 3)
	gpDrain(host)
	gpDrain(p1)
	gpDrain(p2)

	if err := host.gp.Start(); err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return s.Lobby && len(s.Seats) == 1 })
	}

	// p1 takes a seat, then leaves — the seat frees on every end.
	if err := p1.gp.TakeSeat(); err != nil {
		t.Fatalf("take: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return len(s.Seats) == 2 })
	}
	if err := p1.gp.LeaveSeat(); err != nil {
		t.Fatalf("leave: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return len(s.Seats) == 1 })
	}

	// With only the host seated, Begin is refused.
	if err := host.gp.Begin(); err == nil {
		t.Fatal("begin with one seated should fail")
	}
}

// TestGomokuPartyLeaveEndsGame: with three seated players, one leaving mid-game
// ends the game as abandoned on every remaining end.
func TestGomokuPartyLeaveEndsGame(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGP(t, url)
	p1 := joinGP(t, url, phrase)
	p2 := joinGP(t, url, phrase)
	gpWaitRoster(t, host, 3)
	gpDrain(host)
	gpDrain(p1)
	gpDrain(p2)

	openAndSeat(t, host, p1, p2)
	if err := host.gp.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	gpWait(t, host, func(s gomokup.State) bool { return s.Playing && uint32(s.TurnID) == uint32(host.client.Self()) })
	if err := host.gp.Place(0, 0); err != nil {
		t.Fatalf("place: %v", err)
	}

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
