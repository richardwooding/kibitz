package mentalpoker

import (
	"math/big"
	"testing"
)

func mustKey(t *testing.T) Key {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestCommutativeAndRoundTrip: the whole scheme rests on E commuting and D
// undoing E.
func TestCommutativeAndRoundTrip(t *testing.T) {
	a, b := mustKey(t), mustKey(t)
	for i := 0; i < DeckSize; i++ {
		m := Token(i)
		if a.Decrypt(a.Encrypt(m)).Cmp(m) != 0 {
			t.Fatalf("round trip failed for card %d", i)
		}
		ab := a.Encrypt(b.Encrypt(m))
		ba := b.Encrypt(a.Encrypt(m))
		if ab.Cmp(ba) != 0 {
			t.Fatalf("encryption does not commute for card %d", i)
		}
	}
}

// TestDealOne: a doubly-encrypted card can be handed to one player by the other
// stripping its layer, and the recipient recovers the card — while the stripper
// never sees plaintext.
func TestDealOne(t *testing.T) {
	a, b := mustKey(t), mustKey(t)
	card := 37
	double := a.Encrypt(b.Encrypt(Token(card))) // m^(ka·kb)

	// Deal to A: B strips its layer, A strips its own.
	toA := b.Decrypt(double) // m^ka
	if Decode(toA) != -1 {
		t.Fatal("B could decode the card it stripped for A (should be encrypted)")
	}
	if got := Decode(a.Decrypt(toA)); got != card {
		t.Fatalf("A recovered card %d, want %d", got, card)
	}
	// Deal to B: A strips, B strips.
	toB := a.Decrypt(double)
	if Decode(toB) != -1 {
		t.Fatal("A could decode the card it stripped for B")
	}
	if got := Decode(b.Decrypt(toB)); got != card {
		t.Fatalf("B recovered card %d, want %d", got, card)
	}
	// Neither party can decode the doubly-encrypted card alone.
	if Decode(a.Decrypt(double)) != -1 || Decode(b.Decrypt(double)) != -1 {
		// (each single-decrypt yields the OTHER's single encryption, not a token)
		t.Fatal("a single strip should not reveal a token")
	}
}

// TestShuffleDealVerify exercises the full protocol: A encrypt+shuffle, B
// encrypt+shuffle, deal a hand, then verify the shuffle at showdown.
func TestShuffleDealVerify(t *testing.T) {
	a, b := mustKey(t), mustKey(t)

	deck1 := EncryptAll(a, FreshDeck())
	if err := Shuffle(deck1); err != nil {
		t.Fatal(err)
	}
	deck2 := EncryptAll(b, deck1)
	if err := Shuffle(deck2); err != nil {
		t.Fatal(err)
	}

	// Deal the top 10 positions to A and verify they are 10 distinct real cards.
	seen := map[int]bool{}
	for j := 0; j < 10; j++ {
		card := Decode(a.Decrypt(b.Decrypt(deck2[j])))
		if card < 0 || seen[card] {
			t.Fatalf("dealt position %d gave invalid/dup card %d", j, card)
		}
		seen[card] = true
	}

	// Showdown: reveal exponents, reconstruct keys, verify the whole deck.
	ra, ok1 := KeyFromExponent(a.Exponent())
	rb, ok2 := KeyFromExponent(b.Exponent())
	if !ok1 || !ok2 {
		t.Fatal("key reconstruction failed")
	}
	if !VerifyDeck(deck2, ra, rb) {
		t.Fatal("honest deck failed verification")
	}
}

// TestVerifyCatchesTampering: a forged deck entry must fail verification.
func TestVerifyCatchesTampering(t *testing.T) {
	a, b := mustKey(t), mustKey(t)
	deck := EncryptAll(b, EncryptAll(a, FreshDeck()))
	// Duplicate a card (replace entry 5 with entry 0) — a classic cheat.
	deck[5] = new(big.Int).Set(deck[0])
	if VerifyDeck(deck, a, b) {
		t.Fatal("verification passed on a deck with a duplicated card")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	orig := EncryptAll(mustKey(t), FreshDeck())
	back := Unmarshal(Marshal(orig))
	for i := range orig {
		if orig[i].Cmp(back[i]) != 0 {
			t.Fatalf("marshal round trip mismatch at %d", i)
		}
	}
}

func TestShuffleIsPermutation(t *testing.T) {
	d := FreshDeck()
	if err := Shuffle(d); err != nil {
		t.Fatal(err)
	}
	seen := make([]bool, DeckSize)
	for _, m := range d {
		i := Decode(m)
		if i < 0 || seen[i] {
			t.Fatalf("shuffle dropped/duped a card: %v", i)
		}
		seen[i] = true
	}
}
