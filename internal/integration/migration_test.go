package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/parley/session"
)

// TestHostMigration: when the host leaves, the surviving player is promoted to
// host in place, the session lives on, a NEW opponent can join (routed to the
// new host), and a fresh game plays end to end. This is the whole host-migration
// path over the real relay + client + mux + game service.
func TestHostMigration(t *testing.T) {
	url := startRelay(t)

	// Host + one player, each with a connect4 mux.
	host, phrase, err := session.Host(testCtx(t), url, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	hostC4 := connect4.New()
	hostMux := service.NewMux(host, hostC4)
	go drainMux(hostMux)

	player, err := session.Join(testCtx(t), url, phrase, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = player.Close() })
	playerC4 := connect4.New()
	playerMux := service.NewMux(player, playerC4)
	promoted := make(chan session.Role, 1)
	go func() {
		for ev := range playerMux.Events() {
			if p, ok := ev.(service.Promoted); ok {
				promoted <- session.Role(0) // signal; value unused
				_ = p
			}
		}
	}()

	// Start a game so seats exist, then the host leaves for good.
	pollStart(t, hostC4.Start)
	waitC4(t, playerC4, func(s connect4.State) bool { return s.Playing })
	_ = host.Close()

	// The player is promoted to host in place.
	select {
	case <-promoted:
	case <-time.After(10 * time.Second):
		t.Fatal("player was not promoted after the host left")
	}
	if player.Role() != session.RoleHost {
		t.Fatalf("promoted player role = %d, want host", player.Role())
	}
	if player.HostID() != player.Self() {
		t.Fatalf("promoted player hostID = %d, want self %d", player.HostID(), player.Self())
	}

	// The relay must process the ClaimHost before the next join routes.
	time.Sleep(200 * time.Millisecond)

	// A new opponent joins — routed to the NEW host, keyed as a player.
	newbie, err := session.Join(testCtx(t), url, phrase, proto.Options()...)
	if err != nil {
		t.Fatalf("new opponent join after migration: %v", err)
	}
	t.Cleanup(func() { _ = newbie.Close() })
	if newbie.HostID() != player.Self() {
		t.Fatalf("new opponent hostID = %d, want migrated host %d", newbie.HostID(), player.Self())
	}
	newC4 := connect4.New()
	newMux := service.NewMux(newbie, newC4)
	go drainMux(newMux)

	// The new host can now start a fresh game with the new opponent and play it.
	pollStart(t, playerC4.Start)
	waitC4(t, newC4, func(s connect4.State) bool { return s.Playing })
	st := waitC4(t, playerC4, func(s connect4.State) bool { return s.Playing })
	if st.P1ID != player.Self() {
		t.Fatalf("new game P1 = %d, want the promoted host %d", st.P1ID, player.Self())
	}
	// A move flows both ways → the migrated session is fully live.
	waitC4(t, playerC4, func(s connect4.State) bool { return uint32(s.TurnID) == uint32(player.Self()) })
	if err := playerC4.Drop(3); err != nil {
		t.Fatalf("post-migration drop: %v", err)
	}
	waitC4(t, newC4, func(s connect4.State) bool { return s.LastCol == 3 })
}

func drainMux(m *service.Mux) {
	for range m.Events() { //nolint:revive // discard
	}
}

func waitC4(t *testing.T, svc *connect4.Service, match func(connect4.State) bool) connect4.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := svc.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", svc.State())
	panic("unreachable")
}
