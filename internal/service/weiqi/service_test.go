package weiqi

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/parley/wire"
)

func idx(row, col int) int { return row*N + col }

// --- service: lifecycle ------------------------------------------------------

type fakeSender struct{ sent [][]byte }

func (f *fakeSender) Broadcast(_ string, body []byte) error {
	f.sent = append(f.sent, body)
	return nil
}
func (f *fakeSender) SendTo(wire.ParticipantID, string, []byte) error { return nil }

type rig struct {
	host, player       *Service
	hostOut, playerOut *fakeSender
	hostEv, playerEv   *[]any
}

func newRig(t *testing.T) *rig {
	t.Helper()
	var hostEv, playerEv []any
	r := &rig{
		host: New(), player: New(),
		hostOut: &fakeSender{}, playerOut: &fakeSender{},
		hostEv: &hostEv, playerEv: &playerEv,
	}
	r.host.Attach(service.Context{
		Send: r.hostOut, Emit: func(e any) { hostEv = append(hostEv, e) },
		Self: 1, HostID: 1, Host: true,
	})
	r.player.Attach(service.Context{
		Send: r.playerOut, Emit: func(e any) { playerEv = append(playerEv, e) },
		Self: 2, HostID: 1, Host: false,
	})
	r.host.MemberKeyed(2, proto.RolePlayer)
	if err := r.host.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	r.pump(t)
	return r
}

// pump delivers pending broadcasts until both queues drain, failing on any
// handler error (desync).
func (r *rig) pump(t *testing.T) {
	t.Helper()
	for len(r.hostOut.sent) > 0 || len(r.playerOut.sent) > 0 {
		hs, ps := r.hostOut.sent, r.playerOut.sent
		r.hostOut.sent, r.playerOut.sent = nil, nil
		for _, b := range hs {
			if err := r.player.HandleFrame(1, b); err != nil {
				t.Fatalf("player handling: %v", err)
			}
		}
		for _, b := range ps {
			if err := r.host.HandleFrame(2, b); err != nil {
				t.Fatalf("host handling: %v", err)
			}
		}
	}
}

func TestServicePlaceSyncs(t *testing.T) {
	r := newRig(t)
	if err := r.host.Place(0, 0); err != nil { // host is P1 (black), moves first
		t.Fatalf("host place: %v", err)
	}
	r.pump(t)
	if r.host.State().Board[0] != 1 || r.player.State().Board[0] != 1 {
		t.Fatalf("boards did not sync: host=%d player=%d",
			r.host.State().Board[0], r.player.State().Board[0])
	}
	if r.player.State().TurnID != 2 {
		t.Fatalf("turn should pass to white (player 2), got %d", r.player.State().TurnID)
	}
}

func TestServiceTwoPassesEndGame(t *testing.T) {
	r := newRig(t)
	if err := r.host.Pass(); err != nil { // black passes
		t.Fatalf("host pass: %v", err)
	}
	r.pump(t)
	if r.host.State().Outcome != "" {
		t.Fatalf("one pass ended the game: %q", r.host.State().Outcome)
	}
	if err := r.player.Pass(); err != nil { // white passes → game over
		t.Fatalf("player pass: %v", err)
	}
	r.pump(t)

	for name, s := range map[string]*Service{"host": r.host, "player": r.player} {
		st := s.State()
		if st.Outcome != "white wins" { // empty board: komi decides
			t.Fatalf("%s outcome = %q, want %q", name, st.Outcome, "white wins")
		}
		if st.Passes != 2 {
			t.Fatalf("%s passes = %d, want 2", name, st.Passes)
		}
		if st.ScoreW != Komi || st.ScoreB != 0 {
			t.Fatalf("%s scores = B %.1f / W %.1f, want 0 / %.1f", name, st.ScoreB, st.ScoreW, Komi)
		}
	}
}

func TestServiceCaptureCounts(t *testing.T) {
	r := newRig(t)
	// Black (0,0), white (1,1) elsewhere, black (0,1)... build a capture:
	// white plays (0,0)? Simpler: drive a real capture sequence.
	moves := []struct {
		who  *Service
		r, c int8
	}{
		{r.host, 1, 0},   // black
		{r.player, 0, 0}, // white at corner
		{r.host, 0, 1},   // black — captures white (0,0)
	}
	for _, m := range moves {
		if err := m.who.Place(m.r, m.c); err != nil {
			t.Fatalf("place (%d,%d): %v", m.r, m.c, err)
		}
		r.pump(t)
	}
	st := r.host.State()
	if st.CapturesB != 1 {
		t.Fatalf("black captures = %d, want 1", st.CapturesB)
	}
	if st.Board[idx(0, 0)] != 0 {
		t.Fatalf("captured white stone still present")
	}
}
