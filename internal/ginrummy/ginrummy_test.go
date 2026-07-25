package ginrummy_test

import (
	"sort"
	"testing"

	"github.com/richardwooding/kibitz/internal/ginrummy"
)

// card builds a card index from a rank (0=Ace..12=King) and suit (0=♠..3=♣).
func card(rank, suit int) int { return suit*13 + rank }

func TestRankSuit(t *testing.T) {
	cases := []struct {
		c, rank, suit int
	}{
		{0, 0, 0},   // A♠
		{12, 12, 0}, // K♠
		{13, 0, 1},  // A♥
		{25, 12, 1}, // K♥
		{26, 0, 2},  // A♦
		{51, 12, 3}, // K♣
		{17, 4, 1},  // 5♥
	}
	for _, tc := range cases {
		if got := ginrummy.Rank(tc.c); got != tc.rank {
			t.Errorf("Rank(%d) = %d, want %d", tc.c, got, tc.rank)
		}
		if got := ginrummy.Suit(tc.c); got != tc.suit {
			t.Errorf("Suit(%d) = %d, want %d", tc.c, got, tc.suit)
		}
	}
}

func TestCardString(t *testing.T) {
	cases := []struct {
		c    int
		want string
	}{
		{0, "A♠"},
		{card(9, 2), "T♦"}, // rank 9 = "10"
		{51, "K♣"},
		{17, "5♥"},
		{card(10, 0), "J♠"},
		{card(11, 3), "Q♣"},
	}
	for _, tc := range cases {
		if got := ginrummy.CardString(tc.c); got != tc.want {
			t.Errorf("CardString(%d) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestDeadwoodValue(t *testing.T) {
	cases := []struct {
		c, want int
	}{
		{card(0, 0), 1},   // Ace = 1
		{card(1, 0), 2},   // 2
		{card(8, 0), 9},   // 9
		{card(9, 0), 10},  // 10
		{card(10, 0), 10}, // J
		{card(11, 1), 10}, // Q
		{card(12, 2), 10}, // K
	}
	for _, tc := range cases {
		if got := ginrummy.DeadwoodValue(tc.c); got != tc.want {
			t.Errorf("DeadwoodValue(%d) = %d, want %d", tc.c, got, tc.want)
		}
	}
}

func TestRunLowAceValid(t *testing.T) {
	// A-2-3 of spades is a valid run.
	hand := []int{card(0, 0), card(1, 0), card(2, 0)}
	d, melds, unmatched := ginrummy.BestMelds(hand)
	if d != 0 {
		t.Fatalf("A-2-3 run: deadwood = %d, want 0", d)
	}
	if len(melds) != 1 || len(melds[0]) != 3 {
		t.Fatalf("A-2-3 run: melds = %v, want one 3-card meld", melds)
	}
	if len(unmatched) != 0 {
		t.Fatalf("A-2-3 run: unmatched = %v, want none", unmatched)
	}
}

func TestRunNoWraparound(t *testing.T) {
	// Q-K-A of spades is NOT a valid run (no wraparound).
	hand := []int{card(11, 0), card(12, 0), card(0, 0)}
	d, melds, _ := ginrummy.BestMelds(hand)
	if len(melds) != 0 {
		t.Fatalf("Q-K-A: melds = %v, want none", melds)
	}
	// Q(10) + K(10) + A(1) = 21 deadwood.
	if d != 21 {
		t.Fatalf("Q-K-A: deadwood = %d, want 21", d)
	}
}

func TestSetDetection(t *testing.T) {
	// Three Aces (♠♥♦) form a set.
	hand := []int{card(0, 0), card(0, 1), card(0, 2)}
	d, melds, _ := ginrummy.BestMelds(hand)
	if d != 0 {
		t.Fatalf("Ace set: deadwood = %d, want 0", d)
	}
	if len(melds) != 1 || len(melds[0]) != 3 {
		t.Fatalf("Ace set: melds = %v, want one 3-card meld", melds)
	}
}

// TestBestMeldsGlobalOptimum constructs a hand where a card (7♠) can belong to
// either a run (7♠-8♠-9♠) or a set (four 7s). A greedy solver that grabs the
// four-of-a-kind set first strands 8♠+9♠ as 17 deadwood; the global optimum
// puts 7♠ in the run and the other three 7s in a set, yielding zero deadwood.
func TestBestMeldsGlobalOptimum(t *testing.T) {
	hand := []int{
		card(6, 0), card(7, 0), card(8, 0), // 7♠ 8♠ 9♠
		card(6, 1), card(6, 2), card(6, 3), // 7♥ 7♦ 7♣
	}
	d, melds, unmatched := ginrummy.BestMelds(hand)
	if d != 0 {
		t.Fatalf("global optimum: deadwood = %d, want 0 (melds=%v unmatched=%v)", d, melds, unmatched)
	}
	if len(melds) != 2 {
		t.Fatalf("global optimum: got %d melds %v, want 2", len(melds), melds)
	}
	if len(unmatched) != 0 {
		t.Fatalf("global optimum: unmatched = %v, want none", unmatched)
	}
}

func TestBestMeldsPartialGreedyTrap(t *testing.T) {
	// Same trap but with extra deadwood so the optimum is non-zero and we can
	// assert the greedy choice (17) is beaten.
	hand := []int{
		card(6, 0), card(7, 0), card(8, 0), // 7♠ 8♠ 9♠ run
		card(6, 1), card(6, 2), card(6, 3), // three more 7s -> set
		card(12, 1), // K♥ pure deadwood (10)
	}
	d := ginrummy.Deadwood(hand)
	if d != 10 {
		t.Fatalf("greedy trap: deadwood = %d, want 10 (only the K)", d)
	}
}

func TestCanKnockBoundary(t *testing.T) {
	// Deadwood exactly 10: a 9-card spade run A..9 plus a lone K♥ (10).
	knockable := []int{
		card(0, 0), card(1, 0), card(2, 0), card(3, 0), card(4, 0),
		card(5, 0), card(6, 0), card(7, 0), card(8, 0), // A..9 spades run
		card(12, 1), // K♥ = 10 deadwood
	}
	if d := ginrummy.Deadwood(knockable); d != 10 {
		t.Fatalf("knockable hand deadwood = %d, want 10", d)
	}
	if !ginrummy.CanKnock(knockable) {
		t.Fatalf("hand with deadwood 10 should be knockable")
	}
	if ginrummy.IsGin(knockable) {
		t.Fatalf("hand with deadwood 10 should not be gin")
	}

	// Deadwood exactly 11: 8-card spade run A..8 plus K♥ (10) and A♦ (1).
	notKnockable := []int{
		card(0, 0), card(1, 0), card(2, 0), card(3, 0),
		card(4, 0), card(5, 0), card(6, 0), card(7, 0), // A..8 spades run
		card(12, 1), // K♥ = 10
		card(0, 2),  // A♦ = 1
	}
	if d := ginrummy.Deadwood(notKnockable); d != 11 {
		t.Fatalf("not-knockable hand deadwood = %d, want 11", d)
	}
	if ginrummy.CanKnock(notKnockable) {
		t.Fatalf("hand with deadwood 11 should not be knockable")
	}
}

func TestIsGin(t *testing.T) {
	// A 10-card single-suit run A..10 is gin (deadwood 0).
	gin := []int{
		card(0, 0), card(1, 0), card(2, 0), card(3, 0), card(4, 0),
		card(5, 0), card(6, 0), card(7, 0), card(8, 0), card(9, 0),
	}
	if d := ginrummy.Deadwood(gin); d != 0 {
		t.Fatalf("gin hand deadwood = %d, want 0", d)
	}
	if !ginrummy.IsGin(gin) {
		t.Fatalf("full-meld hand should be gin")
	}
	if !ginrummy.CanKnock(gin) {
		t.Fatalf("gin hand should be knockable")
	}
}

func TestScoreGin(t *testing.T) {
	knocker := []int{ // gin: full 10-card run, deadwood 0
		card(0, 0), card(1, 0), card(2, 0), card(3, 0), card(4, 0),
		card(5, 0), card(6, 0), card(7, 0), card(8, 0), card(9, 0),
	}
	opp := []int{card(12, 0), card(4, 1)} // K♠(10) + 5♥(5) = 15 deadwood
	if d := ginrummy.Deadwood(opp); d != 15 {
		t.Fatalf("opp deadwood = %d, want 15", d)
	}
	kp, op := ginrummy.Score(knocker, opp, true)
	if kp != 40 || op != 0 { // 15 + 25 gin bonus
		t.Fatalf("gin score = (%d, %d), want (40, 0)", kp, op)
	}
}

func TestScoreNormalKnock(t *testing.T) {
	knocker := []int{card(3, 0), card(0, 1)} // 4♠(4) + A♥(1) = 5
	opp := []int{card(12, 0), card(9, 1)}    // K♠(10) + T♥(10) = 20
	if kd := ginrummy.Deadwood(knocker); kd != 5 {
		t.Fatalf("knocker deadwood = %d, want 5", kd)
	}
	if od := ginrummy.Deadwood(opp); od != 20 {
		t.Fatalf("opp deadwood = %d, want 20", od)
	}
	kp, op := ginrummy.Score(knocker, opp, false)
	if kp != 15 || op != 0 { // 20 - 5
		t.Fatalf("normal knock score = (%d, %d), want (15, 0)", kp, op)
	}
}

func TestScoreUndercut(t *testing.T) {
	knocker := []int{card(4, 0), card(2, 1)} // 5♠(5) + 3♥(3) = 8
	opp := []int{card(3, 0), card(0, 1)}     // 4♠(4) + A♥(1) = 5
	if kd := ginrummy.Deadwood(knocker); kd != 8 {
		t.Fatalf("knocker deadwood = %d, want 8", kd)
	}
	if od := ginrummy.Deadwood(opp); od != 5 {
		t.Fatalf("opp deadwood = %d, want 5", od)
	}
	kp, op := ginrummy.Score(knocker, opp, false)
	if kp != 0 || op != 28 { // (8 - 5) + 25 undercut bonus
		t.Fatalf("undercut score = (%d, %d), want (0, 28)", kp, op)
	}
}

// containsAll reports whether laid contains exactly the wanted cards (order
// independent).
func containsAll(laid, want []int) bool {
	if len(laid) != len(want) {
		return false
	}
	got := append([]int(nil), laid...)
	w := append([]int(nil), want...)
	sort.Ints(got)
	sort.Ints(w)
	for i := range w {
		if got[i] != w[i] {
			return false
		}
	}
	return true
}

// TestLayOffRunExtensionCascade lays the defender's 4♠ onto the knocker's
// 5♠6♠7♠ run, which then cascades to admit 3♠ on a later pass. K♥ cannot lay
// off and remains as deadwood.
func TestLayOffRunExtensionCascade(t *testing.T) {
	melds := [][]int{{card(4, 0), card(5, 0), card(6, 0)}} // 5♠ 6♠ 7♠ run
	// Order 3♠ before 4♠ so 3♠ only fits after 4♠ extends the run: cascade
	// across passes.
	deadwood := []int{card(2, 0), card(3, 0), card(12, 1)} // 3♠, 4♠, K♥
	remaining, laid := ginrummy.LayOff(deadwood, melds)
	if remaining != 10 { // only K♥ left
		t.Fatalf("run cascade: remaining = %d, want 10", remaining)
	}
	if !containsAll(laid, []int{card(2, 0), card(3, 0)}) {
		t.Fatalf("run cascade: laid = %v, want 3♠ and 4♠", laid)
	}
}

// TestLayOffSet lays the defender's spare K♣ onto the knocker's K set (which
// grows to 4). The 5♥ stays as deadwood.
func TestLayOffSet(t *testing.T) {
	melds := [][]int{{card(12, 0), card(12, 1), card(12, 2)}} // K♠ K♥ K♦ set
	deadwood := []int{card(12, 3), card(4, 1)}                // K♣, 5♥
	remaining, laid := ginrummy.LayOff(deadwood, melds)
	if remaining != 5 { // only 5♥ left
		t.Fatalf("set lay-off: remaining = %d, want 5", remaining)
	}
	if !containsAll(laid, []int{card(12, 3)}) {
		t.Fatalf("set lay-off: laid = %v, want K♣", laid)
	}
}

// TestScoreGinNoLayOff verifies that a gin knock counts the opponent's FULL
// deadwood with no lay-off, even though J♠ would otherwise extend the knocker's
// spade run.
func TestScoreGinNoLayOff(t *testing.T) {
	knocker := []int{ // gin: A..10 spades run, deadwood 0
		card(0, 0), card(1, 0), card(2, 0), card(3, 0), card(4, 0),
		card(5, 0), card(6, 0), card(7, 0), card(8, 0), card(9, 0),
	}
	// J♠ is adjacent to the run's high end, but gin forbids lay-off.
	opp := []int{card(10, 0), card(4, 1)} // J♠(10) + 5♥(5) = 15
	kp, op := ginrummy.Score(knocker, opp, true)
	if kp != 40 || op != 0 { // 15 + 25 gin bonus, no lay-off reduction
		t.Fatalf("gin no-lay-off score = (%d, %d), want (40, 0)", kp, op)
	}
}

// TestScoreLayOffFlipsToUndercut shows lay-off changing the outcome: without it
// the knocker (deadwood 2) beats the opponent's raw deadwood 8 for +6. But the
// opponent's 4♠ and 3♠ lay off onto the knocker's 5♠6♠7♠ run (cascade), leaving
// only A♥ = 1 deadwood, which undercuts the knocker.
func TestScoreLayOffFlipsToUndercut(t *testing.T) {
	knocker := []int{card(4, 0), card(5, 0), card(6, 0), card(1, 1)} // 5♠6♠7♠ run + 2♥(2)
	opp := []int{card(2, 0), card(3, 0), card(0, 1)}                 // 3♠, 4♠, A♥
	if kd := ginrummy.Deadwood(knocker); kd != 2 {
		t.Fatalf("knocker deadwood = %d, want 2", kd)
	}
	if od := ginrummy.Deadwood(opp); od != 8 { // raw, pre-lay-off: 3+4+1
		t.Fatalf("opp raw deadwood = %d, want 8", od)
	}
	kp, op := ginrummy.Score(knocker, opp, false)
	if kp != 0 || op != 26 { // post-lay-off od=1; (2-1)+25 undercut
		t.Fatalf("lay-off undercut score = (%d, %d), want (0, 26)", kp, op)
	}
}

func TestBestMeldsEmpty(t *testing.T) {
	d, melds, unmatched := ginrummy.BestMelds(nil)
	if d != 0 || melds != nil || unmatched != nil {
		t.Fatalf("empty hand = (%d, %v, %v), want (0, nil, nil)", d, melds, unmatched)
	}
}

// TestBestMeldsUnmatchedComplete verifies that meld cards plus unmatched cards
// account for the whole hand exactly once.
func TestBestMeldsUnmatchedComplete(t *testing.T) {
	hand := []int{
		card(6, 0), card(7, 0), card(8, 0),
		card(6, 1), card(6, 2), card(6, 3),
		card(12, 1), card(0, 2),
	}
	_, melds, unmatched := ginrummy.BestMelds(hand)
	var got []int
	got = append(got, unmatched...)
	for _, m := range melds {
		got = append(got, m...)
	}
	sort.Ints(got)
	want := append([]int(nil), hand...)
	sort.Ints(want)
	if len(got) != len(want) {
		t.Fatalf("card accounting: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("card accounting: got %v, want %v", got, want)
		}
	}
}
