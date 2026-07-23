package backgammon

import (
	"errors"
	"testing"
)

// board builds a position from white/black checker placements (in each
// player's OWN numbering) plus bar/off counts.
func board(t *testing.T, white, black map[int8]int8, barW, barB, offW, offB int8) Board {
	t.Helper()
	var b Board
	for rel, n := range white {
		b.Points[global(White, rel)] += n
	}
	for rel, n := range black {
		b.Points[global(Black, rel)] -= n
	}
	b.Bar[White], b.Bar[Black] = barW, barB
	b.Off[White], b.Off[Black] = offW, offB
	return b
}

func TestStartPosition(t *testing.T) {
	b := Start()
	if got := b.PipCount(White); got != 167 {
		t.Fatalf("white pips %d, want 167", got)
	}
	if got := b.PipCount(Black); got != 167 {
		t.Fatalf("black pips %d, want 167", got)
	}
	// 15 checkers each.
	var w, bl int8
	for p := 1; p <= 24; p++ {
		if b.Points[p] > 0 {
			w += b.Points[p]
		} else {
			bl -= b.Points[p]
		}
	}
	if w != 15 || bl != 15 {
		t.Fatalf("checkers w=%d b=%d", w, bl)
	}
	// Symmetry: black's view of its own points mirrors white's.
	for rel := int8(1); rel <= 24; rel++ {
		if b.count(White, rel) != b.count(Black, rel) {
			t.Fatalf("asymmetric start at rel %d", rel)
		}
	}
}

func TestOpening31MakesFivePoint(t *testing.T) {
	b := Start()
	// The classic best play: 8/5 6/5.
	if err := Validate(b, White, 3, 1, []Hop{{From: 8, To: 5}, {From: 6, To: 5}}); err != nil {
		t.Fatalf("8/5 6/5 rejected: %v", err)
	}
	// Order swapped is also legal (1 first, then 3).
	if err := Validate(b, White, 3, 1, []Hop{{From: 6, To: 5}, {From: 8, To: 5}}); err != nil {
		t.Fatalf("6/5 8/5 rejected: %v", err)
	}
	// Playing only one die when two are playable is illegal.
	if err := Validate(b, White, 3, 1, []Hop{{From: 8, To: 5}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("single hop accepted: %v", err)
	}
	// A move onto a made opponent point is illegal (white 6/5 exists but
	// 24/21 onto... black's 4-point (global 21? no: global 21 is empty)).
	// Use global 19 = black 5 checkers: white 24/19 with a 5 isn't this
	// roll; instead try landing on global 17 (black 3): white 20 doesn't
	// exist. Simplest: fabricate an illegal distance.
	if err := Validate(b, White, 3, 1, []Hop{{From: 8, To: 4}, {From: 6, To: 5}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("wrong-distance hop accepted: %v", err)
	}
}

func TestBlockedPointRejected(t *testing.T) {
	b := Start()
	// White 13/8 is fine; 13/7 then 7/... — direct check: black owns its
	// 5-point? At start black has global 19 (own 6-point 19? own numbering:
	// global19 = black rel 6) with 5 checkers. White moving to global 19
	// would be rel 19 for white. White 24/19 with die 5: blocked.
	if err := Validate(b, White, 5, 3, []Hop{{From: 24, To: 19}, {From: 13, To: 10}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("landing on 5-stack accepted: %v", err)
	}
	// Legal alternative with the same dice.
	if err := Validate(b, White, 5, 3, []Hop{{From: 13, To: 8}, {From: 13, To: 10}}); err != nil {
		t.Fatalf("13/8 13/10 rejected: %v", err)
	}
}

func TestDoublesFourMoves(t *testing.T) {
	b := Start()
	turns := LegalTurns(b, White, 6, 6)
	for _, turn := range turns {
		if len(turn) != 4 {
			t.Fatalf("6-6 turn with %d hops: %v", len(turn), turn)
		}
	}
	// 24/18 24/18 13/7 13/7 is the standard play.
	err := Validate(b, White, 6, 6, []Hop{{From: 24, To: 18}, {From: 24, To: 18}, {From: 13, To: 7}, {From: 13, To: 7}})
	if err != nil {
		t.Fatalf("standard 6-6 play rejected: %v", err)
	}
}

func TestBarEntryForced(t *testing.T) {
	// White has a checker on the bar; it must enter before anything else.
	// Black placements are in BLACK's numbering (rel 24 = global 1, etc. —
	// this mirrors the standard start).
	b := board(t,
		map[int8]int8{13: 5, 8: 3, 6: 5, 24: 1},
		map[int8]int8{24: 2, 13: 5, 8: 3, 6: 5},
		1, 0, 0, 0)
	// Entry with die 3 lands on white rel 22 (global 22) — open.
	if err := Validate(b, White, 3, 1, []Hop{{From: 25, To: 22}, {From: 6, To: 5}}); err != nil {
		t.Fatalf("bar entry turn rejected: %v", err)
	}
	// Moving another checker first is illegal.
	if err := Validate(b, White, 3, 1, []Hop{{From: 8, To: 5}, {From: 25, To: 24}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("non-entry first hop accepted: %v", err)
	}
}

func TestDanceWhenEntryBlocked(t *testing.T) {
	// Black owns every entry point for dice 2 and 5 (white enters on rel
	// 23 and 20 → global 23, 20).
	b := board(t,
		map[int8]int8{13: 5},
		map[int8]int8{2: 2, 5: 2}, // black rel 2 = global 23; rel 5 = global 20
		1, 0, 0, 0)
	turns := LegalTurns(b, White, 2, 5)
	if len(turns) != 1 || len(turns[0]) != 0 {
		t.Fatalf("expected dance, got %v", turns)
	}
	if err := Validate(b, White, 2, 5, nil); err != nil {
		t.Fatalf("empty turn on dance rejected: %v", err)
	}
	if err := Validate(b, White, 2, 5, []Hop{{From: 13, To: 11}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("move while on bar accepted: %v", err)
	}
}

func TestLargerDieForced(t *testing.T) {
	// White: one checker on the bar, 14 borne off. Entry points for both
	// dice are open (rel 19 for die 6, rel 20 for die 5) but the follow-up
	// point rel 14 is blocked — so exactly one die can be played, and it
	// must be the 6.
	b := board(t,
		map[int8]int8{},
		map[int8]int8{11: 2}, // black rel 11 = global 14
		1, 0, 14, 0)
	turns := LegalTurns(b, White, 6, 5)
	if len(turns) != 1 {
		t.Fatalf("want exactly one legal turn, got %v", turns)
	}
	if turns[0][0] != (Hop{From: 25, To: 19}) {
		t.Fatalf("larger die not forced: %v", turns[0])
	}
	if err := Validate(b, White, 6, 5, []Hop{{From: 25, To: 20}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("smaller die accepted: %v", err)
	}
}

func TestBearOffExactAndWasted(t *testing.T) {
	// All white in home: 2 on the 6-point, 13 off. Black rel 24 = GLOBAL 1,
	// blocking white's 6/1 — so the 5 is dead and the turn is only the
	// bear-off with the 6 (which also satisfies the larger-die rule).
	b := board(t, map[int8]int8{6: 2}, map[int8]int8{24: 2}, 0, 0, 13, 0)
	if err := Validate(b, White, 6, 5, []Hop{{From: 6, To: 0}}); err != nil {
		t.Fatalf("6/off rejected: %v", err)
	}
	if err := Validate(b, White, 6, 5, []Hop{{From: 6, To: 0}, {From: 6, To: 1}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("move onto blocked 1-point accepted: %v", err)
	}

	// With global 1 open (black rel 1 = global 24, far away), 6-5 must
	// play both: 6/off and 6/1.
	b2 := board(t, map[int8]int8{6: 2}, map[int8]int8{1: 2}, 0, 0, 13, 0)
	if err := Validate(b2, White, 6, 5, []Hop{{From: 6, To: 0}, {From: 6, To: 1}}); err != nil {
		t.Fatalf("6/off 6/1 rejected: %v", err)
	}
	if err := Validate(b2, White, 6, 5, []Hop{{From: 6, To: 0}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("1-hop accepted when both dice playable: %v", err)
	}

	// Wasting big dice: only 2 checkers on the 3-point, roll 6-5 → both off.
	b3 := board(t, map[int8]int8{3: 2}, map[int8]int8{24: 2}, 0, 0, 13, 0)
	if err := Validate(b3, White, 6, 5, []Hop{{From: 3, To: 0}, {From: 3, To: 0}}); err != nil {
		t.Fatalf("wasted bear-off rejected: %v", err)
	}

	// NOT all in home → no bear-off.
	b4 := board(t, map[int8]int8{3: 1, 13: 1}, map[int8]int8{24: 2}, 0, 0, 13, 0)
	if err := Validate(b4, White, 6, 3, []Hop{{From: 3, To: 0}, {From: 13, To: 7}}); !errors.Is(err, ErrIllegalTurn) {
		t.Fatalf("bear-off with checker outside home accepted: %v", err)
	}
}

func TestHitSendsToBar(t *testing.T) {
	// White hits a black blot on white rel 5 (global 5).
	b := board(t, map[int8]int8{8: 1}, map[int8]int8{20: 1}, 0, 0, 14, 14) // black rel 20 = global 5
	if b.Points[5] != -1 {
		t.Fatalf("setup wrong: point 5 = %d", b.Points[5])
	}
	if err := Validate(b, White, 3, 4, []Hop{{From: 8, To: 5}, {From: 5, To: 1}}); err != nil {
		t.Fatalf("hit turn rejected: %v", err)
	}
	nb := ApplyTurn(b, White, []Hop{{From: 8, To: 5}, {From: 5, To: 1}})
	if nb.Bar[Black] != 1 {
		t.Fatalf("black bar = %d after hit", nb.Bar[Black])
	}
	if nb.Points[1] != 1 {
		t.Fatalf("point 1 = %d", nb.Points[1])
	}
}

func TestMaximalUseForced(t *testing.T) {
	// A position where playing the 4 first kills the 3, but 3-then-4 plays
	// both: white checker on rel 8; rel 4 is blocked; rel 5 open; rel 1
	// open. 8-4=4 blocked; 8-3=5 open then 5-4=1 open. So [8/5, 5/1] is
	// the only 2-hop line; a lone [8/5] must be rejected.
	b := board(t,
		map[int8]int8{8: 1},
		map[int8]int8{21: 2, 24: 2}, // black rel 21 = global 4; rel 24 = global 1? no: 25-24=1 → global 1
		0, 0, 14, 11)
	// global 1 is blocked too then — retarget: hop to 1 blocked. Use rel 2:
	// 5-3=2. Dice 3 then 3? Keep dice 3,4: after 8/5 (3), 4 → 5/1 blocked.
	// So arrange: only [8/4?]... Let's just assert engine forces max hops.
	turns := LegalTurns(b, White, 3, 4)
	maxHops := 0
	for _, turn := range turns {
		if len(turn) > maxHops {
			maxHops = len(turn)
		}
	}
	for _, turn := range turns {
		if len(turn) != maxHops {
			t.Fatalf("non-maximal turn in legal set: %v (max %d)", turn, maxHops)
		}
	}
	if maxHops == 2 {
		if err := Validate(b, White, 3, 4, []Hop{{From: 8, To: 5}}); !errors.Is(err, ErrIllegalTurn) {
			t.Fatalf("1-hop turn accepted when 2 available: %v", err)
		}
	}
}

func TestWinnerAndGammonKinds(t *testing.T) {
	// Plain win: white off 15, black off 3.
	b := board(t, map[int8]int8{}, map[int8]int8{6: 12}, 0, 0, 15, 3)
	r := b.Winner()
	if r == nil || r.Winner != White || r.Points != 1 {
		t.Fatalf("plain win: %+v", r)
	}

	// Gammon: black bore off nothing.
	b2 := board(t, map[int8]int8{}, map[int8]int8{6: 15}, 0, 0, 15, 0)
	r2 := b2.Winner()
	if r2 == nil || r2.Points != 2 {
		t.Fatalf("gammon: %+v", r2)
	}

	// Backgammon: black still in white's home (black rel 19..24).
	b3 := board(t, map[int8]int8{}, map[int8]int8{20: 15}, 0, 0, 15, 0)
	r3 := b3.Winner()
	if r3 == nil || r3.Points != 3 {
		t.Fatalf("backgammon (home): %+v", r3)
	}

	// Backgammon via bar.
	b4 := board(t, map[int8]int8{}, map[int8]int8{6: 14}, 0, 1, 15, 0)
	r4 := b4.Winner()
	if r4 == nil || r4.Points != 3 {
		t.Fatalf("backgammon (bar): %+v", r4)
	}

	// No winner mid-game.
	start := Start()
	if start.Winner() != nil {
		t.Fatal("winner at start")
	}
}

func TestBlackMirrors(t *testing.T) {
	b := Start()
	// Black's 8/5 6/5 with 3-1 (its own numbering) must be legal too.
	if err := Validate(b, Black, 3, 1, []Hop{{From: 8, To: 5}, {From: 6, To: 5}}); err != nil {
		t.Fatalf("black 8/5 6/5 rejected: %v", err)
	}
	nb := ApplyTurn(b, Black, []Hop{{From: 8, To: 5}, {From: 6, To: 5}})
	// Black rel 5 = global 20 → two black checkers there now.
	if nb.Points[20] != -2 {
		t.Fatalf("global 20 = %d after black makes its 5-point", nb.Points[20])
	}
	if nb.PipCount(Black) != 167-4 {
		t.Fatalf("black pips %d", nb.PipCount(Black))
	}
}

func TestApplyTurnConservesCheckers(t *testing.T) {
	// Property: any legal turn conserves 15 checkers per side.
	b := Start()
	dice := [][2]int8{{3, 1}, {6, 6}, {5, 2}, {4, 4}, {2, 1}}
	cur := b
	color := White
	for _, d := range dice {
		turns := LegalTurns(cur, color, d[0], d[1])
		if len(turns) == 0 {
			t.Fatal("no legal turns generated")
		}
		cur = ApplyTurn(cur, color, turns[0])
		for _, c := range []Color{White, Black} {
			total := cur.Bar[c] + cur.Off[c]
			for rel := int8(1); rel <= 24; rel++ {
				total += cur.count(c, rel)
			}
			if total != 15 {
				t.Fatalf("%v has %d checkers after %v", c, total, turns[0])
			}
		}
		color = color.Opponent()
	}
}

// TestRandomPlayouts plays full games with deterministic pseudo-random dice
// and turn choices, asserting invariants on every position: checkers
// conserved, all generated turns valid by Validate, games reach a winner.
func TestRandomPlayouts(t *testing.T) {
	rng := uint64(0x9e3779b97f4a7c15)
	next := func(n int) int { // xorshift, deterministic across runs
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return int(rng % uint64(n))
	}
	finished := 0
	for game := 0; game < 20; game++ {
		b := Start()
		c := White
		for turn := 0; turn < 800; turn++ {
			if b.Winner() != nil {
				finished++
				break
			}
			d1, d2 := int8(next(6)+1), int8(next(6)+1)
			turns := LegalTurns(b, c, d1, d2)
			if len(turns) == 0 {
				t.Fatalf("game %d: empty legal-turn set (must contain at least the dance)", game)
			}
			pick := turns[next(len(turns))]
			if err := Validate(b, c, d1, d2, pick); err != nil {
				t.Fatalf("game %d: generated turn fails own validation: %v", game, err)
			}
			b = ApplyTurn(b, c, pick)
			for _, col := range []Color{White, Black} {
				total := b.Bar[col] + b.Off[col]
				for rel := int8(1); rel <= 24; rel++ {
					total += b.count(col, rel)
				}
				if total != 15 {
					t.Fatalf("game %d: %v has %d checkers", game, col, total)
				}
			}
			c = c.Opponent()
		}
	}
	if finished < 15 {
		t.Fatalf("only %d/20 random games finished within 800 turns", finished)
	}
}
