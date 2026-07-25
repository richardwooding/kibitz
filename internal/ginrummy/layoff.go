package ginrummy

// LayOff models the defender's lay-off after a non-gin knock: the defender may
// place their own unmatched (deadwood) cards onto the knocker's melds, reducing
// the deadwood value counted against them.
//
// deadwood is the defender's unmatched card indices (as returned by BestMelds);
// knockerMelds is the knocker's melds (also from BestMelds). A card may join a
// SET of its rank (a set caps at 4 cards) or extend either END of a RUN with a
// same-suit adjacent card. Run extensions cascade (extending 5♠6♠7♠ with 4♠
// then admits 3♠), so lay-off iterates to a fixed point. Ace is low only,
// matching BestMelds: there is no Q-K-A wraparound.
//
// It returns the remaining deadwood VALUE (the sum of DeadwoodValue over the
// cards NOT laid off) and the list of card indices that were laid off.
func LayOff(deadwood []int, knockerMelds [][]int) (remaining int, laid []int) {
	groups := buildGroups(knockerMelds)
	left := append([]int(nil), deadwood...)
	for {
		still, got := layOffPass(left, groups)
		if len(got) == 0 {
			break
		}
		laid = append(laid, got...)
		left = still
	}
	for _, c := range left {
		remaining += DeadwoodValue(c)
	}
	return remaining, laid
}

// meldGroup is a mutable lay-off target derived from one of the knocker's melds.
// For a set, rank is the shared rank and count is the current size (caps at 4).
// For a run, suit is the shared suit and [lo, hi] is the current rank span.
type meldGroup struct {
	isSet bool
	rank  int
	count int
	suit  int
	lo    int
	hi    int
}

// buildGroups classifies each valid meld (length >= 3) into a mutable target.
func buildGroups(melds [][]int) []*meldGroup {
	groups := make([]*meldGroup, 0, len(melds))
	for _, m := range melds {
		if len(m) < 3 {
			continue
		}
		g := classifyMeld(m)
		groups = append(groups, &g)
	}
	return groups
}

// classifyMeld decides whether a meld is a set (all one rank) or a run, and
// captures the state lay-off will mutate.
func classifyMeld(m []int) meldGroup {
	r0 := Rank(m[0])
	lo, hi := r0, r0
	allSame := true
	for _, c := range m {
		r := Rank(c)
		if r != r0 {
			allSame = false
		}
		if r < lo {
			lo = r
		}
		if r > hi {
			hi = r
		}
	}
	if allSame {
		return meldGroup{isSet: true, rank: r0, count: len(m)}
	}
	return meldGroup{suit: Suit(m[0]), lo: lo, hi: hi}
}

// layOffPass makes a single pass over the remaining deadwood, laying off every
// card that currently fits a group and returning the survivors plus the cards
// laid off this pass. Callers repeat passes so run extensions cascade.
func layOffPass(left []int, groups []*meldGroup) (still, laid []int) {
	for _, c := range left {
		if tryLayOff(c, groups) {
			laid = append(laid, c)
		} else {
			still = append(still, c)
		}
	}
	return still, laid
}

// tryLayOff attempts to place card c onto any group, mutating that group on
// success.
func tryLayOff(c int, groups []*meldGroup) bool {
	for _, g := range groups {
		if g.isSet {
			if laySet(c, g) {
				return true
			}
			continue
		}
		if layRun(c, g) {
			return true
		}
	}
	return false
}

// laySet adds c to a set of its rank while the set has room (max 4 cards).
func laySet(c int, g *meldGroup) bool {
	if g.count < 4 && Rank(c) == g.rank {
		g.count++
		return true
	}
	return false
}

// layRun extends a run by c when c is the same suit and sits just below the low
// end or just above the high end. Ace is low only, so no wraparound is possible.
func layRun(c int, g *meldGroup) bool {
	if Suit(c) != g.suit {
		return false
	}
	r := Rank(c)
	if r == g.lo-1 {
		g.lo = r
		return true
	}
	if r == g.hi+1 {
		g.hi = r
		return true
	}
	return false
}
