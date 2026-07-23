package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/battleship"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/shipcommit"
)

type bsTable struct {
	client *session.Client
	mux    *service.Mux
	bs     *battleship.Service
}

func newBSTable(t *testing.T, c *session.Client) *bsTable {
	t.Helper()
	bs := battleship.New()
	tb := &bsTable{client: c, mux: service.NewMux(c, bs), bs: bs}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func bsWait(t *testing.T, tb *bsTable, match func(battleship.State) bool) battleship.State {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.bs.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v phase=%s)", tb.bs.State().Outcome, tb.bs.State().Phase)
	panic("unreachable")
}

func rowsPlacement() [100]uint8 {
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

// TestBattleshipFullGameOverRelay: full game over a live relay with a
// mid-game late joiner. Host knows the player used the row placement and
// snipes all 17 ship cells; the player answers into water.
func TestBattleshipFullGameOverRelay(t *testing.T) {
	url := startRelay(t)
	hc, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	host := newBSTable(t, hc)

	jc, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jc.Close() })
	player := newBSTable(t, jc)

	pollStart(t, host.bs.Start)
	bsWait(t, player, func(s battleship.State) bool { return s.Phase == "placing" })

	random, err := shipcommit.RandomPlacement()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.bs.Commit(random); err != nil {
		t.Fatal(err)
	}
	if err := player.bs.Commit(rowsPlacement()); err != nil {
		t.Fatal(err)
	}
	for _, tb := range []*bsTable{host, player} {
		bsWait(t, tb, func(s battleship.State) bool { return s.Phase == "shooting" })
	}

	// A spectator joins mid-game and must pick up both commit vectors via
	// snapshot to verify reveals it never saw the start of.
	sc, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	spec := newBSTable(t, sc)
	bsWait(t, spec, func(s battleship.State) bool { return s.Phase == "shooting" })

	shipCells := []uint8{}
	for i, id := range rowsPlacement() {
		if id != 0 {
			shipCells = append(shipCells, uint8(i))
		}
	}
	water := uint8(99)
	for _, cell := range shipCells {
		st := bsWait(t, host, func(s battleship.State) bool {
			return s.TurnID == host.client.Self() || s.Phase != "shooting"
		})
		if st.Phase != "shooting" {
			break
		}
		if err := host.bs.Shoot(cell); err != nil {
			t.Fatalf("host shoot %d: %v", cell, err)
		}
		st = bsWait(t, player, func(s battleship.State) bool {
			return s.TurnID == player.client.Self() || s.Phase != "shooting"
		})
		if st.Phase != "shooting" {
			break
		}
		if err := player.bs.Shoot(water); err != nil {
			t.Fatalf("player shoot: %v", err)
		}
		water--
	}

	// Everyone (spectator too — it verified every reveal + both full boards)
	// converges on a clean player-1 win.
	for _, tb := range []*bsTable{host, player, spec} {
		st := bsWait(t, tb, func(s battleship.State) bool { return s.Phase == "over" })
		if st.Outcome != "player 1 wins" {
			t.Fatalf("outcome %q", st.Outcome)
		}
		if st.CheatBy != 0 {
			t.Fatalf("cheat flagged in honest game")
		}
		if len(st.Sunk[1]) != 5 {
			t.Fatalf("sunk list %v", st.Sunk[1])
		}
	}
}
