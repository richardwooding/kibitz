package ginrummy

import "sort"

// BestMelds partitions a hand so each card belongs to at most one meld, choosing
// the decomposition that minimises the total deadwood value of the unmatched
// cards. It returns that minimal deadwood, one optimal set of melds (each a
// slice of card indices), and the unmatched (deadwood) cards.
//
// A card may be usable in either a set or a run but not both; the optimum is
// found by an exhaustive backtracking search over all candidate melds (hand
// sizes are <= 11, so this is cheap), memoised on the set of resolved cards.
func BestMelds(hand []int) (deadwood int, melds [][]int, unmatched []int) {
	if len(hand) == 0 {
		return 0, nil, nil
	}
	s := newSolver(hand)
	best := s.solve(0)
	melds = make([][]int, len(best.melds))
	covered := make([]bool, len(hand))
	for k, m := range best.melds {
		cards := make([]int, len(m))
		for j, idx := range m {
			cards[j] = hand[idx]
			covered[idx] = true
		}
		melds[k] = cards
	}
	for i, c := range hand {
		if !covered[i] {
			unmatched = append(unmatched, c)
		}
	}
	return best.cost, melds, unmatched
}

// partial is a search result: the minimal deadwood cost for a set of resolved
// cards and the melds chosen to achieve it (each meld is a slice of hand
// indices).
type partial struct {
	cost  int
	melds [][]int
}

// solver holds the candidate melds for a hand and memoises the backtracking
// search keyed by the bitmask of resolved (matched or written-off) cards.
type solver struct {
	hand    []int
	cands   [][]int // candidate melds, each a slice of hand indices
	byIndex [][]int // byIndex[i] = candidate indices whose meld contains card i
	memo    map[uint16]partial
}

func newSolver(hand []int) *solver {
	s := &solver{hand: hand, memo: make(map[uint16]partial)}
	s.generateSets()
	s.generateRuns()
	s.indexMelds()
	return s
}

// generateSets emits every candidate set (3 or 4 cards of equal rank).
func (s *solver) generateSets() {
	byRank := make([][]int, 13)
	for i, c := range s.hand {
		r := Rank(c)
		byRank[r] = append(byRank[r], i)
	}
	for _, idxs := range byRank {
		s.emitSets(idxs)
	}
}

// emitSets adds the full group (if 3 or 4) plus, for a group of 4, each 3-card
// subset — so a fourth same-rank card can be freed for use in a run instead.
func (s *solver) emitSets(idxs []int) {
	if len(idxs) < 3 {
		return
	}
	s.cands = append(s.cands, clone(idxs))
	if len(idxs) != 4 {
		return
	}
	for skip := 0; skip < 4; skip++ {
		sub := make([]int, 0, 3)
		for j := 0; j < 4; j++ {
			if j != skip {
				sub = append(sub, idxs[j])
			}
		}
		s.cands = append(s.cands, sub)
	}
}

// generateRuns emits every candidate run (3+ consecutive same-suit cards).
func (s *solver) generateRuns() {
	bySuit := make([][]int, 4)
	for i, c := range s.hand {
		su := Suit(c)
		bySuit[su] = append(bySuit[su], i)
	}
	for _, idxs := range bySuit {
		s.runsInSuit(idxs)
	}
}

// runsInSuit finds maximal blocks of consecutive ranks within one suit and
// emits every contiguous sub-run of length >= 3 from each block.
func (s *solver) runsInSuit(idxs []int) {
	sort.Slice(idxs, func(a, b int) bool {
		return Rank(s.hand[idxs[a]]) < Rank(s.hand[idxs[b]])
	})
	for j := 0; j < len(idxs); {
		k := j + 1
		for k < len(idxs) && Rank(s.hand[idxs[k]]) == Rank(s.hand[idxs[k-1]])+1 {
			k++
		}
		s.emitSubRuns(idxs[j:k])
		j = k
	}
}

// emitSubRuns adds every contiguous slice of block with length 3 or more.
func (s *solver) emitSubRuns(block []int) {
	for start := 0; start < len(block); start++ {
		for end := start + 3; end <= len(block); end++ {
			s.cands = append(s.cands, clone(block[start:end]))
		}
	}
}

// indexMelds builds the reverse lookup from a card index to the candidate melds
// that contain it.
func (s *solver) indexMelds() {
	s.byIndex = make([][]int, len(s.hand))
	for ci, m := range s.cands {
		for _, idx := range m {
			s.byIndex[idx] = append(s.byIndex[idx], ci)
		}
	}
}

// solve returns the minimal-deadwood partition of the cards not yet resolved.
// resolved is a bitmask of card indices already assigned to a meld or written
// off as deadwood.
func (s *solver) solve(resolved uint16) partial {
	if v, ok := s.memo[resolved]; ok {
		return v
	}
	i := firstUnresolved(resolved, len(s.hand))
	if i < 0 {
		return partial{}
	}
	best := s.asDeadwood(i, resolved)
	for _, ci := range s.byIndex[i] {
		m := s.cands[ci]
		if overlaps(m, resolved) {
			continue
		}
		sub := s.solve(resolved | maskOf(m))
		if sub.cost < best.cost {
			best = partial{sub.cost, prepend(m, sub.melds)}
		}
	}
	s.memo[resolved] = best
	return best
}

// asDeadwood resolves card i as unmatched and returns the resulting partition.
func (s *solver) asDeadwood(i int, resolved uint16) partial {
	sub := s.solve(resolved | (1 << uint(i)))
	return partial{sub.cost + DeadwoodValue(s.hand[i]), sub.melds}
}

func firstUnresolved(resolved uint16, n int) int {
	for i := 0; i < n; i++ {
		if resolved&(1<<uint(i)) == 0 {
			return i
		}
	}
	return -1
}

func maskOf(m []int) uint16 {
	var mask uint16
	for _, idx := range m {
		mask |= 1 << uint(idx)
	}
	return mask
}

func overlaps(m []int, resolved uint16) bool {
	return maskOf(m)&resolved != 0
}

func prepend(m []int, rest [][]int) [][]int {
	out := make([][]int, 0, len(rest)+1)
	out = append(out, m)
	return append(out, rest...)
}

func clone(idxs []int) []int {
	return append([]int(nil), idxs...)
}
