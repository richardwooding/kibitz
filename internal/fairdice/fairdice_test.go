package fairdice

import (
	"crypto/sha256"
	"testing"
)

func TestRollRoundTrip(t *testing.T) {
	reveal, commit, err := NewRoll()
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(commit, reveal) {
		t.Fatal("honest reveal failed verification")
	}
}

func TestCheatingRevealDetected(t *testing.T) {
	reveal, commit, err := NewRoll()
	if err != nil {
		t.Fatal(err)
	}
	// The roller dislikes the dice and tries a different RA.
	cheat := reveal
	cheat.RA[0] ^= 1
	if Verify(commit, cheat) {
		t.Fatal("tampered RA passed verification")
	}
	cheat = reveal
	cheat.Salt[3] ^= 0x80
	if Verify(commit, cheat) {
		t.Fatal("tampered salt passed verification")
	}
	if Verify(Commit{1, 2, 3}, reveal) {
		t.Fatal("mismatched commit passed verification")
	}
}

func TestDiceBoundsAndDeterminism(t *testing.T) {
	reveal, _, err := NewRoll()
	if err != nil {
		t.Fatal(err)
	}
	rb, err := NewResponse()
	if err != nil {
		t.Fatal(err)
	}
	d1, d2 := Dice(reveal, rb)
	if d1 < 1 || d1 > 6 || d2 < 1 || d2 > 6 {
		t.Fatalf("dice out of range: %d, %d", d1, d2)
	}
	e1, e2 := Dice(reveal, rb)
	if e1 != d1 || e2 != d2 {
		t.Fatal("same inputs, different dice")
	}
}

// TestResponderCannotSteer: for a fixed (unknown to responder) RA, changing
// RB changes the dice unpredictably — pin that many RBs give a healthy
// spread. This is a smoke test of the derivation, not a randomness proof.
func TestDiceRoughlyUniform(t *testing.T) {
	reveal, _, err := NewRoll()
	if err != nil {
		t.Fatal(err)
	}
	counts := [7]int{}
	const rolls = 6000
	var rb Response
	for i := 0; i < rolls; i++ {
		// Deterministic RB sequence so the test can't flake on rand.
		rb = sha256.Sum256(append(rb[:], byte(i)))
		d1, d2 := Dice(reveal, rb)
		counts[d1]++
		counts[d2]++
	}
	// Each face expects 2*6000/6 = 2000; allow ±15%.
	for face := 1; face <= 6; face++ {
		if counts[face] < 1700 || counts[face] > 2300 {
			t.Fatalf("face %d appeared %d times in %d dice (expected ~2000)", face, counts[face], 2*rolls)
		}
	}
}

func TestOpeningNeverEqual(t *testing.T) {
	reveal, _, err := NewRoll()
	if err != nil {
		t.Fatal(err)
	}
	var rb Response
	for i := 0; i < 2000; i++ {
		rb = sha256.Sum256(append(rb[:], byte(i)))
		a, b := Opening(reveal, rb)
		if a == b {
			t.Fatalf("opening produced equal dice %d,%d", a, b)
		}
		if a < 1 || a > 6 || b < 1 || b > 6 {
			t.Fatalf("opening out of range: %d,%d", a, b)
		}
		// Deterministic.
		a2, b2 := Opening(reveal, rb)
		if a2 != a || b2 != b {
			t.Fatal("opening not deterministic")
		}
	}
}

// The commitment must be over the deterministic CBOR encoding — two encodes
// of the same reveal must hash identically (this is why wire pins
// deterministic mode).
func TestCommitmentStable(t *testing.T) {
	reveal, _, err := NewRoll()
	if err != nil {
		t.Fatal(err)
	}
	c1, err := reveal.Commitment()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := reveal.Commitment()
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Fatal("commitment not stable across encodes")
	}
}
