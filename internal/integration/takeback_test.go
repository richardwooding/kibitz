package integration

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/service/connect4"
)

// TestConnectFourTakeback is the reference takeback flow over the relay: the
// last mover offers, the opponent accepts, and both ends revert one move
// (board, turn, and history all roll back).
func TestConnectFourTakeback(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostC4(t, url)
	player := joinC4(t, url, phrase)
	pollStart(t, host.c4.Start)
	for _, tb := range []*c4Table{host, player} {
		c4Wait(t, tb, func(s connect4.State) bool { return s.Playing })
	}

	c4Wait(t, host, func(s connect4.State) bool { return uint32(s.TurnID) == uint32(host.client.Self()) })
	if err := host.c4.Drop(3); err != nil { // host = red = P1
		t.Fatal(err)
	}
	c4Wait(t, player, func(s connect4.State) bool { return s.LastCol == 3 })

	// Host just moved, so it (not on turn) may offer a takeback.
	c4Wait(t, host, func(s connect4.State) bool { return s.CanTakeback })
	if err := host.c4.OfferTakeback(); err != nil {
		t.Fatalf("offer: %v", err)
	}
	// The opponent sees the pending offer, then accepts.
	c4Wait(t, player, func(s connect4.State) bool { return uint32(s.TakebackBy) == uint32(host.client.Self()) })
	if err := player.c4.AcceptTakeback(); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// Both ends revert: empty board, no history, host to move again.
	for _, tb := range []*c4Table{host, player} {
		st := c4Wait(t, tb, func(s connect4.State) bool { return s.Playing && len(s.History) == 0 })
		if st.Board != (connect4.Board{}) {
			t.Fatalf("board not reverted: %v", st.Board)
		}
		if st.TurnID != host.client.Self() {
			t.Fatalf("turn = %d, want host after revert", st.TurnID)
		}
		if st.TakebackBy != 0 {
			t.Fatalf("takeback offer not cleared: %d", st.TakebackBy)
		}
	}
}
