// Package ginrummy is a pure-logic rules and scoring library for two-player
// Gin Rummy, operating on plain card indices (0..51) with no crypto,
// networking, or service dependencies. Everything here is deterministic (no
// randomness) so callers can drive and verify games reproducibly.
//
// Card model: a card is an int 0..51 where Rank(c) = c % 13 (0=Ace .. 12=King)
// and Suit(c) = c / 13 (0=♠, 1=♥, 2=♦, 3=♣).
//
// Melds are the scoring groups: a set is 3 or 4 cards of the same rank, and a
// run is 3+ consecutive cards of the same suit with Ace LOW only — A-2-3 is a
// valid run but Q-K-A is not (there is no wraparound). BestMelds finds the
// decomposition that minimises leftover deadwood.
//
// Lay-off: after a non-gin knock the defender may lay their own deadwood cards
// onto the knocker's melds, reducing the defender's counted deadwood before
// scoring (see LayOff). Score applies this in the non-gin branch, so the
// opponent deadwood (od) it compares is the post-lay-off remainder. Gin scoring
// is unaffected because a gin opponent has no deadwood and cannot lay off.
package ginrummy

// HandTarget is the match target: the first player to reach this cumulative
// score wins the match. Callers own match bookkeeping; this package only scores
// a single hand.
const HandTarget = 100

// GinBonus is added to the knocker's score for going gin.
const GinBonus = 25

// UndercutBonus is added to the opponent's score when they undercut the knocker.
const UndercutBonus = 25

const rankChars = "A23456789TJQK"

var suitRunes = []rune{'♠', '♥', '♦', '♣'}

// Rank returns the rank of card c: 0=Ace, 1=2, .. 8=9, 9=10, 10=J, 11=Q, 12=K.
func Rank(c int) int { return c % 13 }

// Suit returns the suit of card c: 0=♠, 1=♥, 2=♦, 3=♣.
func Suit(c int) int { return c / 13 }

// CardString renders a card as rank+suit, e.g. "A♠", "T♦", "K♣", "5♥".
func CardString(c int) string {
	return string(rankChars[Rank(c)]) + string(suitRunes[Suit(c)])
}

// DeadwoodValue is the point value of a card as deadwood: Ace=1, 2..10 = pip
// value (rank+1), and J/Q/K = 10.
func DeadwoodValue(c int) int {
	r := Rank(c)
	if r >= 10 {
		return 10
	}
	return r + 1
}

// Deadwood returns the minimal total deadwood value of a hand, i.e. the
// deadwood component of BestMelds.
func Deadwood(hand []int) int {
	d, _, _ := BestMelds(hand)
	return d
}

// CanKnock reports whether a 10-card hand may knock: its minimal deadwood is at
// most 10.
func CanKnock(hand []int) bool {
	return Deadwood(hand) <= 10
}

// IsGin reports whether a hand is gin: it melds completely with zero deadwood.
func IsGin(hand []int) bool {
	return Deadwood(hand) == 0
}

// Score computes the points for a single completed hand between the knocker and
// their opponent. gin indicates the knocker went gin (all cards melded).
//
// Let kd = Deadwood(knocker). In the non-gin case od is the opponent's deadwood
// AFTER laying off onto the knocker's melds (see LayOff); for gin it is the
// opponent's full deadwood (a gin opponent cannot lay off).
//   - Gin: knocker scores od + GinBonus; opponent scores 0.
//   - Normal knock (kd < od): knocker scores od - kd; opponent scores 0.
//   - Undercut (kd >= od, non-gin): opponent scores (kd - od) + UndercutBonus;
//     knocker scores 0.
func Score(knocker, opponent []int, gin bool) (knockerPts, oppPts int) {
	kd := Deadwood(knocker)
	if gin {
		return Deadwood(opponent) + GinBonus, 0
	}
	_, knockerMelds, _ := BestMelds(knocker)
	_, _, oppDeadwood := BestMelds(opponent)
	od, _ := LayOff(oppDeadwood, knockerMelds)
	if kd < od {
		return od - kd, 0
	}
	return 0, (kd - od) + UndercutBonus
}
