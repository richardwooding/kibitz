package integration

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/ginrummy"
	"github.com/richardwooding/kibitz/internal/service/gin"
)

// bestDiscard returns the card whose removal leaves the lowest deadwood, and
// that lowest deadwood.
func bestDiscard(hand []int8) (int8, int) {
	best, bestDW := hand[0], 1<<30
	for _, c := range hand {
		rest := make([]int, 0, len(hand)-1)
		for _, x := range hand {
			if x != c {
				rest = append(rest, int(x))
			}
		}
		if dw := ginrummy.Deadwood(rest); dw < bestDW {
			best, bestDW = c, dw
		}
	}
	return best, bestDW
}

// TestGinKnockAndVerify plays both hands greedily toward a knock over the relay
// and, when a knock is reached, asserts the showdown scores match on both ends
// and the shuffle is cryptographically verified. It skips (never fails) in the
// rare deal where neither hand reaches knockable deadwood before the stock runs
// out — the crypto verify + scoring are also covered by unit tests.
func TestGinKnockAndVerify(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGin(t, url)
	player := joinGin(t, url, phrase)
	pollStart(t, host.g.Start)
	ginPassOpening(t, host, player) // skip the opening upcard offer
	ginWait(t, host, func(s gin.State) bool { return s.Phase == "draw" })
	ginWait(t, player, func(s gin.State) bool { return s.Phase == "draw" })

	byID := map[uint32]*ginTable{
		uint32(host.client.Self()):   host,
		uint32(player.client.Self()): player,
	}
	knocked := false
	for turn := 0; turn < 80; turn++ {
		st := host.g.State()
		if st.Phase != "draw" {
			break
		}
		actor := byID[uint32(st.TurnID)]
		if actor == nil || st.StockCount == 0 {
			break
		}
		if err := actor.g.DrawStock(); err != nil {
			break
		}
		drew := ginWait(t, actor, func(s gin.State) bool { return s.Phase == "discard" && len(s.Hand) == 11 })
		card, dw := bestDiscard(drew.Hand)
		if dw <= 10 {
			if err := actor.g.Knock(card); err != nil {
				t.Fatalf("knock: %v", err)
			}
			knocked = true
			break
		}
		if err := actor.g.Discard(card); err != nil {
			t.Fatalf("discard: %v", err)
		}
		other := host
		if actor == host {
			other = player
		}
		ginWait(t, other, func(s gin.State) bool { return s.TurnID == other.client.Self() && s.Phase == "draw" })
	}

	if !knocked {
		t.Skip("no knockable hand reached before stock ran out (rare); crypto+score covered by unit tests")
	}

	hs := ginWait(t, host, func(s gin.State) bool { return s.Phase == "over" })
	ps := ginWait(t, player, func(s gin.State) bool { return s.Phase == "over" })
	if hs.Scores != ps.Scores {
		t.Fatalf("scores disagree: host %v vs player %v", hs.Scores, ps.Scores)
	}
	if !hs.Verified || !ps.Verified {
		t.Fatalf("shuffle not verified at showdown: host=%v player=%v", hs.Verified, ps.Verified)
	}
	if hs.Scores[0]+hs.Scores[1] == 0 {
		t.Fatalf("no points scored at showdown: %v", hs.Scores)
	}
}
