// Backgammon rules engine — pure logic, no protocol, no I/O. Hand-rolled
// because no maintained Go backgammon library exists. Both clients run this
// engine and validate every turn (there is no trusted server), so turn
// validation is generate-all-legal-turns + membership, which encodes the
// awkward tabletop obligations (use both dice if possible; if only one can
// be played, the larger) without special cases. Doubling cube: deferred.
package backgammon

import (
	"errors"
	"fmt"
)

// Color identifies a player. White moves from point 24 toward 1 (in its own
// numbering) and bears off from 1..6; Black mirrors.
type Color int8

const (
	White Color = 0
	Black Color = 1
)

func (c Color) Opponent() Color { return 1 - c }

func (c Color) String() string {
	if c == White {
		return "white"
	}
	return "black"
}

// Board is the shared position. Points are numbered 1..24 in WHITE's
// numbering (Black's point p is 25-p); positive counts are White checkers,
// negative are Black.
type Board struct {
	Points [25]int8 `cbor:"1,keyasint"` // index 0 unused
	Bar    [2]int8  `cbor:"2,keyasint"`
	Off    [2]int8  `cbor:"3,keyasint"`
}

// Start is the standard opening position.
func Start() Board {
	var b Board
	// White (positive), in White's numbering.
	b.Points[24], b.Points[13], b.Points[8], b.Points[6] = 2, 5, 3, 5
	// Black (negative), mirrored.
	b.Points[1], b.Points[12], b.Points[17], b.Points[19] = -2, -5, -3, -5
	return b
}

// Hop is one checker movement in the MOVING player's own numbering:
// From 1..24 for a point, 25 for the bar; To 1..24, or 0 for bearing off.
type Hop struct {
	From int8 `cbor:"1,keyasint"`
	To   int8 `cbor:"2,keyasint"`
}

const (
	barFrom = 25
	offTo   = 0
	homeTop = 6 // home board is own points 1..6
)

// global translates a player-relative point (1..24) to the shared numbering.
func global(c Color, rel int8) int8 {
	if c == White {
		return rel
	}
	return 25 - rel
}

// count returns c's checkers on its own point rel (1..24) or the bar (25).
func (b *Board) count(c Color, rel int8) int8 {
	if rel == barFrom {
		return b.Bar[c]
	}
	v := b.Points[global(c, rel)]
	if c == White {
		if v > 0 {
			return v
		}
		return 0
	}
	if v < 0 {
		return -v
	}
	return 0
}

// oppCount returns the opponent's checkers on c's point rel.
func (b *Board) oppCount(c Color, rel int8) int8 {
	return b.count(c.Opponent(), 25-rel)
}

// allInHome reports whether c may bear off: nothing on the bar, nothing
// outside its home board.
func (b *Board) allInHome(c Color) bool {
	if b.Bar[c] > 0 {
		return false
	}
	for rel := int8(homeTop + 1); rel <= 24; rel++ {
		if b.count(c, rel) > 0 {
			return false
		}
	}
	return true
}

// highest returns c's highest occupied point (0 when none).
func (b *Board) highest(c Color) int8 {
	for rel := int8(24); rel >= 1; rel-- {
		if b.count(c, rel) > 0 {
			return rel
		}
	}
	return 0
}

// legalHops enumerates every single hop c can make with one die.
func (b *Board) legalHops(c Color, die int8) []Hop {
	var hops []Hop
	// Checkers on the bar must enter first.
	if b.Bar[c] > 0 {
		to := int8(25) - die
		if b.oppCount(c, to) <= 1 {
			hops = append(hops, Hop{From: barFrom, To: to})
		}
		return hops
	}
	for rel := int8(1); rel <= 24; rel++ {
		if b.count(c, rel) == 0 {
			continue
		}
		if to := rel - die; to >= 1 {
			if b.oppCount(c, to) <= 1 {
				hops = append(hops, Hop{From: rel, To: to})
			}
			continue
		}
		// Bear-off territory (rel <= die).
		if !b.allInHome(c) {
			continue
		}
		if rel == die || rel == b.highest(c) {
			// Exact roll, or wasting a larger die from the highest point.
			hops = append(hops, Hop{From: rel, To: offTo})
		}
	}
	return hops
}

// apply mutates the board with one hop (assumed legal for c).
func (b *Board) apply(c Color, h Hop) {
	// Lift the checker.
	if h.From == barFrom {
		b.Bar[c]--
	} else {
		g := global(c, h.From)
		if c == White {
			b.Points[g]--
		} else {
			b.Points[g]++
		}
	}
	// Land it.
	if h.To == offTo {
		b.Off[c]++
		return
	}
	g := global(c, h.To)
	// Hit a blot: opponent checker goes to their bar.
	if b.oppCount(c, h.To) == 1 {
		b.Points[g] = 0
		b.Bar[c.Opponent()]++
	}
	if c == White {
		b.Points[g]++
	} else {
		b.Points[g]--
	}
}

// LegalTurns generates every complete legal turn for c with the given dice.
// A turn is an ordered hop sequence; the tabletop obligations fall out of
// the construction:
//   - all returned sequences have maximal length (use both dice if you can),
//   - when only one die can be played and either could be, only the larger
//     die's hops are returned,
//   - an empty single sequence means no legal moves (a dance).
func LegalTurns(b Board, c Color, d1, d2 int8) [][]Hop {
	var orders [][]int8
	if d1 == d2 {
		orders = [][]int8{{d1, d1, d1, d1}}
	} else {
		orders = [][]int8{{d1, d2}, {d2, d1}}
	}

	var all [][]Hop
	seen := map[string]bool{}
	for _, dice := range orders {
		dfs(b, c, dice, nil, &all, seen)
	}

	maxLen := 0
	for _, t := range all {
		if len(t) > maxLen {
			maxLen = len(t)
		}
	}
	var out [][]Hop
	for _, t := range all {
		if len(t) == maxLen {
			out = append(out, t)
		}
	}

	// Larger-die rule: if only one hop is playable and both dice have
	// playable hops as openers, keep only the larger die's.
	if maxLen == 1 && d1 != d2 {
		hi := max(d1, d2)
		var larger [][]Hop
		for _, t := range out {
			if hopUsesDie(b, c, t[0], hi) {
				larger = append(larger, t)
			}
		}
		if len(larger) > 0 {
			out = larger
		}
	}
	if maxLen == 0 {
		return [][]Hop{{}} // dance: the only legal turn is the empty one
	}
	return out
}

// hopUsesDie reports whether hop h can be a play of the given die on board b.
func hopUsesDie(b Board, c Color, h Hop, die int8) bool {
	for _, lh := range b.legalHops(c, die) {
		if lh == h {
			return true
		}
	}
	return false
}

func dfs(b Board, c Color, dice []int8, prefix []Hop, all *[][]Hop, seen map[string]bool) {
	if len(dice) == 0 {
		record(prefix, all, seen)
		return
	}
	hops := b.legalHops(c, dice[0])
	if len(hops) == 0 {
		// This die is dead; later dice in the order can't resurrect it
		// (orders cover both permutations), so the sequence ends here —
		// except doubles, where remaining identical dice are dead too.
		record(prefix, all, seen)
		return
	}
	for _, h := range hops {
		nb := b
		nb.apply(c, h)
		dfs(nb, c, dice[1:], append(append([]Hop{}, prefix...), h), all, seen)
	}
}

func record(prefix []Hop, all *[][]Hop, seen map[string]bool) {
	key := fmt.Sprint(prefix)
	if seen[key] {
		return
	}
	seen[key] = true
	*all = append(*all, append([]Hop{}, prefix...))
}

var ErrIllegalTurn = errors.New("backgammon: illegal turn")

// Validate checks a submitted complete turn against the dice, encoding
// membership in the legal-turn set (order matters: a hop may only be legal
// because an earlier hop opened it).
func Validate(b Board, c Color, d1, d2 int8, turn []Hop) error {
	for _, legal := range LegalTurns(b, c, d1, d2) {
		if hopsEqual(legal, turn) {
			return nil
		}
	}
	return ErrIllegalTurn
}

func hopsEqual(a, b []Hop) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ApplyTurn returns the board after a (validated) turn.
func ApplyTurn(b Board, c Color, turn []Hop) Board {
	for _, h := range turn {
		b.apply(c, h)
	}
	return b
}

// Result of a finished game.
type Result struct {
	Winner Color
	// Points: 1 plain, 2 gammon (loser bore off nothing), 3 backgammon
	// (gammon + loser has a checker in the winner's home or on the bar).
	Points int
}

// Winner returns the game result, or nil while play continues.
func (b *Board) Winner() *Result {
	for _, c := range []Color{White, Black} {
		if b.Off[c] != 15 {
			continue
		}
		r := &Result{Winner: c, Points: 1}
		loser := c.Opponent()
		if b.Off[loser] == 0 {
			r.Points = 2
			if b.Bar[loser] > 0 || b.loserInWinnersHome(loser) {
				r.Points = 3
			}
		}
		return r
	}
	return nil
}

// loserInWinnersHome: does the loser still have a checker in the winner's
// home board? In the loser's own numbering that is points 19..24.
func (b *Board) loserInWinnersHome(loser Color) bool {
	for rel := int8(19); rel <= 24; rel++ {
		if b.count(loser, rel) > 0 {
			return true
		}
	}
	return false
}

// PipCount is the classic race metric (sum of distances to bear off).
func (b *Board) PipCount(c Color) int {
	pips := int(b.Bar[c]) * 25
	for rel := int8(1); rel <= 24; rel++ {
		pips += int(b.count(c, rel)) * int(rel)
	}
	return pips
}
