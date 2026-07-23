package battleship

import (
	"strings"
	"testing"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/shipcommit"
	"github.com/richardwooding/kibitz/internal/wire"
)

type fakeSender struct{ sent [][]byte }

func (f *fakeSender) Broadcast(_ string, body []byte) error {
	f.sent = append(f.sent, body)
	return nil
}
func (f *fakeSender) SendTo(wire.ParticipantID, string, []byte) error { return nil }

// rig wires host (participant 1) and player (participant 2) services with an
// in-memory pipe, starts a game, and returns both plus their event logs.
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
	r.host.MemberKeyed(2, session.RolePlayer)
	if err := r.host.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	r.pump(t, false)
	return r
}

// pump delivers pending broadcasts to the other side. strict=false ignores
// handler errors (cheat tests expect them at specific points instead).
func (r *rig) pump(t *testing.T, strict bool) {
	t.Helper()
	for len(r.hostOut.sent) > 0 || len(r.playerOut.sent) > 0 {
		hs, ps := r.hostOut.sent, r.playerOut.sent
		r.hostOut.sent, r.playerOut.sent = nil, nil
		for _, b := range hs {
			if err := r.player.HandleFrame(1, b); err != nil && strict {
				t.Fatalf("player handling: %v", err)
			}
		}
		for _, b := range ps {
			if err := r.host.HandleFrame(2, b); err != nil && strict {
				t.Fatalf("host handling: %v", err)
			}
		}
	}
}

func placementRows() [100]uint8 {
	var p [100]uint8
	for i := 0; i < 5; i++ {
		p[i] = 1
	}
	for i := 0; i < 4; i++ {
		p[10+i] = 2
	}
	for i := 0; i < 3; i++ {
		p[20+i] = 3
	}
	for i := 0; i < 3; i++ {
		p[30+i] = 4
	}
	for i := 0; i < 2; i++ {
		p[40+i] = 5
	}
	return p
}

func commitBoth(t *testing.T, r *rig) {
	t.Helper()
	if err := r.host.Commit(placementRows()); err != nil {
		t.Fatal(err)
	}
	if err := r.player.Commit(placementRows()); err != nil {
		t.Fatal(err)
	}
	r.pump(t, true)
	if r.host.State().Phase != "shooting" || r.player.State().Phase != "shooting" {
		t.Fatalf("phases: host=%s player=%s", r.host.State().Phase, r.player.State().Phase)
	}
}

func TestHappyPathToVictory(t *testing.T) {
	r := newRig(t)
	commitBoth(t, r)

	// P1 (host) knows the mirror placement — shoot all 17 ship cells; P2
	// answers each turn by shooting water (cell 99 downward).
	shipCells := []uint8{}
	for i, id := range placementRows() {
		if id != 0 {
			shipCells = append(shipCells, uint8(i))
		}
	}
	waterCell := uint8(99)
	for _, cell := range shipCells {
		if err := r.host.Shoot(cell); err != nil {
			t.Fatalf("host shoot %d: %v", cell, err)
		}
		r.pump(t, true)
		if r.host.State().Phase != "shooting" {
			break // fleet sunk mid-loop
		}
		if err := r.player.Shoot(waterCell); err != nil {
			t.Fatalf("player shoot: %v", err)
		}
		waterCell -= 1
		r.pump(t, true)
	}

	for _, s := range []*Service{r.host, r.player} {
		st := s.State()
		if st.Phase != "over" {
			t.Fatalf("phase %s, want over", st.Phase)
		}
		if st.Outcome != "player 1 wins" {
			t.Fatalf("outcome %q", st.Outcome)
		}
		if st.CheatBy != 0 {
			t.Fatalf("cheat flagged in honest game: %d", st.CheatBy)
		}
		if len(st.Sunk[1]) != 5 {
			t.Fatalf("sunk ships on player board: %v", st.Sunk[1])
		}
	}
}

func TestWrongSaltRevealDetected(t *testing.T) {
	r := newRig(t)
	commitBoth(t, r)

	// Host shoots a ship cell; the PLAYER lies with a doctored reveal
	// (claims water with a fresh salt).
	if err := r.host.Shoot(0); err != nil {
		t.Fatal(err)
	}
	// Deliver the shot to the player but intercept its honest reveal.
	r.playerOut.sent = nil
	for _, b := range r.hostOut.sent {
		_ = r.player.HandleFrame(1, b)
	}
	r.hostOut.sent = nil
	// Forge: water at cell 0.
	lie := shipcommit.CellReveal{Cell: 0, ShipID: 0}
	body, err := wire.Marshal(msg{Kind: kindReveal, Reveal: &lie})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.host.HandleFrame(2, body); err == nil || !strings.Contains(err.Error(), "commitment") {
		t.Fatalf("forged reveal accepted: %v", err)
	}
	st := r.host.State()
	if st.CheatBy != 2 || st.Outcome != "voided — cheating detected" {
		t.Fatalf("cheat not flagged: %+v", st)
	}
}

func TestWrongCellRevealRejected(t *testing.T) {
	r := newRig(t)
	commitBoth(t, r)
	if err := r.host.Shoot(5); err != nil {
		t.Fatal(err)
	}
	r.playerOut.sent = nil
	for _, b := range r.hostOut.sent {
		_ = r.player.HandleFrame(1, b)
	}
	r.hostOut.sent = nil

	// The player reveals a DIFFERENT (honest) cell — must be rejected as
	// unexpected, not accepted for the shot.
	honest := shipcommit.CellReveal{Cell: 50, ShipID: 0}
	body, _ := wire.Marshal(msg{Kind: kindReveal, Reveal: &honest})
	if err := r.host.HandleFrame(2, body); err == nil {
		t.Fatal("reveal for wrong cell accepted")
	}
}

func TestOutOfTurnShotRejected(t *testing.T) {
	r := newRig(t)
	commitBoth(t, r)
	// P2 shoots first — but P1 has the opening shot.
	if err := r.player.Shoot(0); err == nil {
		t.Fatal("out-of-turn shot accepted locally")
	}
	body, _ := wire.Marshal(msg{Kind: kindShot, Cell: 0})
	if err := r.host.HandleFrame(2, body); err == nil {
		t.Fatal("out-of-turn shot accepted by peer")
	}
}

func TestDoubleCommitRejected(t *testing.T) {
	r := newRig(t)
	if err := r.host.Commit(placementRows()); err != nil {
		t.Fatal(err)
	}
	if err := r.host.Commit(placementRows()); err == nil {
		t.Fatal("second commit accepted locally")
	}
	r.pump(t, true)
	// A second commit over the wire is rejected too.
	_, commits, err := shipcommit.NewBoard(placementRows())
	if err != nil {
		t.Fatal(err)
	}
	flat := make([][]byte, 100)
	for i := range commits {
		flat[i] = commits[i][:]
	}
	body, _ := wire.Marshal(msg{Kind: kindCommitBoard, Commits: flat})
	if err := r.player.HandleFrame(1, body); err == nil {
		t.Fatal("peer double commit accepted")
	}
}

func TestIllegalFleetCaughtAtValidation(t *testing.T) {
	// Player commits a board with NO ships (all water) — undetectable at
	// commit time (commitments hide content) and unhittable in play, but
	// the fullReveal legality check must catch it. Rig it: player builds a
	// custom Board manually bypassing NewBoard's legality gate.
	r := newRig(t)
	if err := r.host.Commit(placementRows()); err != nil {
		t.Fatal(err)
	}
	// Hand-build the cheating player's water-only board + commitments.
	var board shipcommit.Board
	var commits [100][32]byte
	for i := 0; i < 100; i++ {
		board.Cells[i] = shipcommit.CellReveal{Cell: uint8(i), ShipID: 0}
		c, err := board.Cells[i].Commitment()
		if err != nil {
			t.Fatal(err)
		}
		commits[i] = c
	}
	r.player.mu.Lock()
	r.player.myBoard = &board
	r.player.commits[1] = commits
	r.player.committed[1] = true
	r.player.maybeStartShootingLocked()
	r.player.mu.Unlock()
	flat := make([][]byte, 100)
	for i := range commits {
		flat[i] = commits[i][:]
	}
	body, _ := wire.Marshal(msg{Kind: kindCommitBoard, Commits: flat})
	if err := r.host.HandleFrame(2, body); err != nil {
		t.Fatal(err)
	}
	r.pump(t, true)

	// Host resigns → validation phase. An HONEST client would refuse to
	// broadcast an illegal fleet (it self-detects); a real cheater runs a
	// modified client, so craft the fullReveal by hand and feed the host.
	if err := r.host.Resign(); err != nil {
		t.Fatal(err)
	}
	r.pump(t, false)
	body, _ = wire.Marshal(msg{Kind: kindFullReveal, Cells: board.Cells[:]})
	if err := r.host.HandleFrame(2, body); err == nil || !strings.Contains(err.Error(), "illegal fleet") {
		t.Fatalf("illegal fleet accepted: %v", err)
	}
	st := r.host.State()
	if st.CheatBy != 2 || st.Outcome != "voided — cheating detected" {
		t.Fatalf("empty fleet not flagged: outcome=%q cheatBy=%d", st.Outcome, st.CheatBy)
	}
}
