package integration

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/service/gin"
)

// TestGinMatchAndOpeningTake exercises the opening upcard-offer TAKE path and
// dealer alternation across hands of a match: the non-dealer takes the opening
// upcard, the hand ends by resignation, and the next hand of the same match
// alternates the deal while carrying the running score forward.
func TestGinMatchAndOpeningTake(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGin(t, url)
	player := joinGin(t, url, phrase)
	pollStart(t, host.g.Start)

	// Hand 1: host (P1) deals, joiner (P2) is offered the upcard first.
	h1 := ginWait(t, host, func(s gin.State) bool { return s.Phase == "upcard-offer" && len(s.Hand) == 10 })
	if h1.DealerID != host.client.Self() {
		t.Fatalf("hand 1 dealer = %d, want host", h1.DealerID)
	}
	ginWait(t, player, func(s gin.State) bool { return s.Phase == "upcard-offer" })

	// The non-dealer TAKES the opening upcard → 11 cards, on to discard.
	if err := player.g.TakeUpcardOffer(); err != nil {
		t.Fatalf("take upcard offer: %v", err)
	}
	ps := ginWait(t, player, func(s gin.State) bool { return s.Phase == "discard" && len(s.Hand) == 11 })
	ginWait(t, host, func(s gin.State) bool { return s.HandCounts[1] == 11 })

	// Discard back to 10, then resign → the opponent (host, P1) is credited.
	if err := player.g.Discard(ps.Hand[0]); err != nil {
		t.Fatalf("discard: %v", err)
	}
	ginWait(t, host, func(s gin.State) bool { return s.Phase == "draw" && s.TurnID == host.client.Self() })
	if err := host.g.Resign(); err != nil {
		t.Fatalf("resign: %v", err)
	}
	over := ginWait(t, player, func(s gin.State) bool { return s.Phase == "over" })
	if over.MatchOver {
		t.Fatalf("match should not be over after one 25-point hand: %+v", over.Scores)
	}
	if over.Scores[1] < 25 { // P2 (player) credited for host's forfeit
		t.Fatalf("forfeit not credited: %+v", over.Scores)
	}
	carried := over.Scores

	// Hand 2 of the same match: the deal alternates to the joiner (P2) and the
	// running score carries over (not a fresh match).
	pollStart(t, host.g.Start)
	h2 := ginWait(t, host, func(s gin.State) bool { return s.Phase == "upcard-offer" && len(s.Hand) == 10 })
	if h2.DealerID != player.client.Self() {
		t.Fatalf("hand 2 dealer = %d, want joiner (alternated)", h2.DealerID)
	}
	if h2.TurnID != host.client.Self() {
		t.Fatalf("hand 2 opening offer to = %d, want host (non-dealer)", h2.TurnID)
	}
	if h2.Scores != carried {
		t.Fatalf("hand 2 scores %v did not carry over from %v", h2.Scores, carried)
	}
}
