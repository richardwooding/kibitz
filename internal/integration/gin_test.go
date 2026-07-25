package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/gin"
	"github.com/richardwooding/kibitz/internal/session"
)

type ginTable struct {
	client *session.Client
	mux    *service.Mux
	g      *gin.Service
}

func hostGin(t *testing.T, url string) (*ginTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	g := gin.New()
	tb := &ginTable{client: c, mux: service.NewMux(c, g), g: g}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb, phrase
}

func joinGin(t *testing.T, url, phrase string) *ginTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	g := gin.New()
	tb := &ginTable{client: c, mux: service.NewMux(c, g), g: g}
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
	return tb
}

func ginWait(t *testing.T, tb *ginTable, match func(gin.State) bool) gin.State {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.g.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.g.State())
	panic("unreachable")
}

// ginPassOpening advances past the opening upcard-offer by having each offer-
// holder (non-dealer, then dealer) pass, leaving the non-dealer on to draw.
func ginPassOpening(t *testing.T, host, player *ginTable) {
	t.Helper()
	byID := map[uint32]*ginTable{
		uint32(host.client.Self()):   host,
		uint32(player.client.Self()): player,
	}
	for pass := 0; pass < 2; pass++ {
		st := ginWait(t, host, func(s gin.State) bool { return s.Phase == "upcard-offer" || s.Phase == "draw" })
		if st.Phase == "draw" {
			return
		}
		actor := byID[uint32(st.TurnID)]
		if actor == nil {
			t.Fatalf("no offer-holder for turn %d", st.TurnID)
		}
		before := st.TurnID
		// Wait for the offer-holder's own view to reach the offer before it acts.
		ginWait(t, actor, func(s gin.State) bool { return s.Phase == "upcard-offer" && s.TurnID == actor.client.Self() })
		if err := actor.g.PassUpcard(); err != nil {
			t.Fatalf("pass upcard: %v", err)
		}
		ginWait(t, host, func(s gin.State) bool { return s.TurnID != before || s.Phase == "draw" })
	}
	ginWait(t, host, func(s gin.State) bool { return s.Phase == "draw" })
	ginWait(t, player, func(s gin.State) bool { return s.Phase == "draw" })
}

// TestGinDealAndPlay proves the dealerless shuffle + reveal protocol over the
// relay: both players get 10 distinct, disjoint, real cards without either (or
// the relay) knowing the other's hand; a stock draw reveals correctly; discard
// and take-upcard flow; resign ends the hand.
func TestGinDealAndPlay(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGin(t, url)
	player := joinGin(t, url, phrase)
	pollStart(t, host.g.Start)

	// Deal completes: both reach the opening upcard-offer holding 10 cards.
	hs := ginWait(t, host, func(s gin.State) bool { return s.Phase == "upcard-offer" && len(s.Hand) == 10 })
	ps := ginWait(t, player, func(s gin.State) bool { return s.Phase == "upcard-offer" && len(s.Hand) == 10 })

	// The two hands plus the upcard are 21 distinct real cards (crypto deal ok).
	seen := map[int8]bool{}
	all := append(append(append([]int8{}, hs.Hand...), ps.Hand...), hs.Discard[len(hs.Discard)-1])
	for _, c := range all {
		if c < 0 || c > 51 || seen[c] {
			t.Fatalf("deal produced invalid/duplicate card %d (hands overlap or bad decode)", c)
		}
		seen[c] = true
	}
	if len(all) != 21 {
		t.Fatalf("expected 21 dealt cards, got %d", len(all))
	}
	// The host (P1) deals; the joiner (P2, non-dealer) is offered the upcard first.
	if hs.DealerID != host.client.Self() {
		t.Fatalf("dealer = %d, want host", hs.DealerID)
	}
	if hs.TurnID != player.client.Self() {
		t.Fatalf("opening offer to = %d, want joiner", hs.TurnID)
	}

	// Both pass the opening; the non-dealer (joiner) is then on to draw.
	ginPassOpening(t, host, player)
	hs = ginWait(t, host, func(s gin.State) bool { return s.Phase == "draw" })
	if hs.TurnID != player.client.Self() {
		t.Fatalf("first draw turn = %d, want joiner", hs.TurnID)
	}

	// P2 draws from stock → 11 cards; the host sees the public counts move.
	if err := player.g.DrawStock(); err != nil {
		t.Fatalf("draw stock: %v", err)
	}
	ps = ginWait(t, player, func(s gin.State) bool { return len(s.Hand) == 11 && s.Phase == "discard" })
	ginWait(t, host, func(s gin.State) bool { return s.HandCounts[1] == 11 && s.StockCount == 30 })

	// P2 discards a card → turn passes to P1, discard top is that card.
	discarded := ps.Hand[0]
	if err := player.g.Discard(discarded); err != nil {
		t.Fatalf("discard: %v", err)
	}
	ginWait(t, host, func(s gin.State) bool {
		return s.TurnID == host.client.Self() && s.Phase == "draw" &&
			len(s.Discard) > 0 && s.Discard[len(s.Discard)-1] == discarded
	})

	// P1 takes the upcard → 11 cards, discard shrinks.
	if err := host.g.TakeUpcard(); err != nil {
		t.Fatalf("take upcard: %v", err)
	}
	ginWait(t, host, func(s gin.State) bool { return len(s.Hand) == 11 && s.Phase == "discard" })

	// Resign ends the hand; the opponent is credited.
	if err := host.g.Discard(host.g.State().Hand[0]); err != nil {
		t.Fatalf("discard back to 10: %v", err)
	}
	if err := host.g.Resign(); err != nil {
		t.Fatalf("resign: %v", err)
	}
	for _, tb := range []*ginTable{host, player} {
		st := ginWait(t, tb, func(s gin.State) bool { return s.Phase == "over" })
		if st.Scores[1] < 25 { // P2 credited for the forfeit
			t.Fatalf("forfeit score not credited: %+v", st.Scores)
		}
	}
}
