package chess

import (
	"errors"
	"strings"
	"testing"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// fakeSender captures broadcasts so two services can be wired back-to-back
// without a relay.
type fakeSender struct{ sent [][]byte }

func (f *fakeSender) Broadcast(serviceID string, body []byte) error {
	f.sent = append(f.sent, body)
	return nil
}
func (f *fakeSender) SendTo(wire.ParticipantID, string, []byte) error { return nil }

// pair wires a host service (participant 1, white) and a player service
// (participant 2, black) with an in-memory pipe: every broadcast from one is
// handed to the other.
type pair struct {
	host, player *Service
	hostEv       []any
	playerEv     []any
	hostOut      *fakeSender
	playerOut    *fakeSender
}

func newPair(t *testing.T) *pair {
	t.Helper()
	p := &pair{host: New(), player: New(), hostOut: &fakeSender{}, playerOut: &fakeSender{}}
	p.host.Attach(service.Context{
		Send: p.hostOut, Emit: func(e any) { p.hostEv = append(p.hostEv, e) },
		Self: 1, HostID: 1, Host: true,
	})
	p.player.Attach(service.Context{
		Send: p.playerOut, Emit: func(e any) { p.playerEv = append(p.playerEv, e) },
		Self: 2, HostID: 1, Host: false,
	})
	// Seat the player, then launch on demand (M3: no auto-start).
	p.host.MemberKeyed(2, session.RolePlayer)
	if err := p.host.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p.pump(t)
	return p
}

// pump delivers all pending broadcasts to the opposite side until quiet.
func (p *pair) pump(t *testing.T) {
	t.Helper()
	for len(p.hostOut.sent) > 0 || len(p.playerOut.sent) > 0 {
		hs, ps := p.hostOut.sent, p.playerOut.sent
		p.hostOut.sent, p.playerOut.sent = nil, nil
		for _, b := range hs {
			if err := p.player.HandleFrame(1, b); err != nil {
				t.Fatalf("player handling host frame: %v", err)
			}
		}
		for _, b := range ps {
			if err := p.host.HandleFrame(2, b); err != nil {
				t.Fatalf("host handling player frame: %v", err)
			}
		}
	}
}

// moveAs plays one move from the given side and pumps it across.
func (p *pair) moveAs(t *testing.T, s *Service, uci string) {
	t.Helper()
	if err := s.TryMove(uci); err != nil {
		t.Fatalf("move %s: %v", uci, err)
	}
	p.pump(t)
}

func TestGameStartsWhenPlayerKeyed(t *testing.T) {
	p := newPair(t)
	st := p.host.State()
	if !st.Playing || st.WhiteID != 1 || st.BlackID != 2 || st.TurnID != 1 {
		t.Fatalf("host state %+v", st)
	}
	pst := p.player.State()
	if !pst.Playing || pst.WhiteID != 1 {
		t.Fatalf("player state %+v", pst)
	}
}

func TestScholarsMate(t *testing.T) {
	p := newPair(t)
	moves := []struct {
		s   *Service
		uci string
	}{
		{p.host, "e2e4"}, {p.player, "e7e5"},
		{p.host, "d1h5"}, {p.player, "b8c6"},
		{p.host, "f1c4"}, {p.player, "g8f6"},
		{p.host, "h5f7"}, // mate
	}
	for _, m := range moves {
		p.moveAs(t, m.s, m.uci)
	}
	for _, s := range []*Service{p.host, p.player} {
		st := s.State()
		if st.Outcome != "1-0" {
			t.Fatalf("outcome %q, want 1-0", st.Outcome)
		}
		if st.Method != "Checkmate" {
			t.Fatalf("method %q", st.Method)
		}
		if st.TurnID != 0 {
			t.Fatalf("turn %d after game over", st.TurnID)
		}
	}
}

func TestIllegalAndOutOfTurnMovesRejected(t *testing.T) {
	p := newPair(t)
	if err := p.host.TryMove("e2e5"); err == nil {
		t.Fatal("illegal pawn jump accepted")
	}
	if err := p.player.TryMove("e7e5"); !errors.Is(err, ErrNotTurn) {
		t.Fatalf("out-of-turn: %v", err)
	}
	// A peer sending an out-of-turn move over the wire is rejected too.
	body, _ := wire.Marshal(msg{Kind: kindMove, UCI: "e7e5"})
	if err := p.host.HandleFrame(2, body); err == nil {
		t.Fatal("host accepted out-of-turn peer move")
	}
}

func TestSpectatorCannotMove(t *testing.T) {
	spec := New()
	var specEv []any
	spec.Attach(service.Context{
		Send: &fakeSender{}, Emit: func(e any) { specEv = append(specEv, e) },
		Self: 3, HostID: 1, Host: false,
	})
	// Spectator syncs via the same newGame the player got.
	body, _ := wire.Marshal(msg{Kind: kindNewGame, WhiteID: 1, BlackID: 2})
	if err := spec.HandleFrame(1, body); err != nil {
		t.Fatal(err)
	}
	if err := spec.TryMove("e2e4"); !errors.Is(err, ErrNotPlayer) {
		t.Fatalf("spectator move: %v", err)
	}
}

func TestStateHashMismatchIsDesync(t *testing.T) {
	p := newPair(t)
	body, _ := wire.Marshal(msg{Kind: kindMove, UCI: "e2e4", StateHash: []byte{1, 2, 3, 4, 5, 6, 7, 8}})
	err := p.player.HandleFrame(1, body)
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("want hash mismatch, got %v", err)
	}
	found := false
	for _, e := range p.playerEv {
		if d, ok := e.(Desync); ok && d.Reason == "position hash mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatal("no Desync event emitted")
	}
}

func TestResign(t *testing.T) {
	p := newPair(t)
	p.moveAs(t, p.host, "e2e4")
	if err := p.player.Resign(); err != nil {
		t.Fatal(err)
	}
	p.pump(t)
	for _, s := range []*Service{p.host, p.player} {
		st := s.State()
		if st.Outcome != "1-0" || st.Method != "Resignation" {
			t.Fatalf("state %+v", st)
		}
	}
}

func TestDrawByAgreement(t *testing.T) {
	p := newPair(t)
	p.moveAs(t, p.host, "e2e4")
	if err := p.player.OfferDraw(); err != nil {
		t.Fatal(err)
	}
	p.pump(t)
	offered := false
	for _, e := range p.hostEv {
		if _, ok := e.(DrawOffered); ok {
			offered = true
		}
	}
	if !offered {
		t.Fatal("host never saw the draw offer")
	}
	if err := p.host.AgreeDraw(); err != nil {
		t.Fatal(err)
	}
	p.pump(t)
	for _, s := range []*Service{p.host, p.player} {
		if st := s.State(); st.Outcome != "1/2-1/2" {
			t.Fatalf("outcome %q", st.Outcome)
		}
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	p := newPair(t)
	for _, m := range []struct {
		s   *Service
		uci string
	}{{p.host, "e2e4"}, {p.player, "c7c5"}, {p.host, "g1f3"}} {
		p.moveAs(t, m.s, m.uci)
	}

	blob, err := p.host.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	late := New()
	late.Attach(service.Context{Send: &fakeSender{}, Emit: func(any) {}, Self: 5, HostID: 1, Host: false})
	if err := late.Restore(blob); err != nil {
		t.Fatal(err)
	}
	if got, want := late.State().FEN, p.host.State().FEN; got != want {
		t.Fatalf("restored FEN %q != %q", got, want)
	}
	if late.State().TurnID != 2 {
		t.Fatalf("turn after restore = %d", late.State().TurnID)
	}
}

func TestLegalTargets(t *testing.T) {
	p := newPair(t)
	targets := p.host.LegalTargets("e2")
	if len(targets) != 2 {
		t.Fatalf("e2 targets %v", targets)
	}
	if p.host.LegalTargets("e5") != nil {
		t.Fatal("empty square has targets")
	}
}

func TestPlayerLeavingForfeits(t *testing.T) {
	p := newPair(t)
	p.moveAs(t, p.host, "e2e4")
	p.host.MemberLeft(2)
	st := p.host.State()
	if st.Outcome != "1-0" || st.Method != "Resignation" {
		t.Fatalf("state %+v", st)
	}
}
