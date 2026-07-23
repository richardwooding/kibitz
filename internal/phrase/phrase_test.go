package phrase

import (
	"encoding/hex"
	"regexp"
	"testing"
)

func TestWordlistLoaded(t *testing.T) {
	// EFF short list is 1296 words; we drop the one hyphenated entry
	// ("yo-yo") so phrases stay cleanly word-NN-word.
	if len(words) != 1295 {
		t.Fatalf("wordlist has %d words, want 1295 (EFF short list minus yo-yo)", len(words))
	}
	for _, w := range words {
		if len(w) == 0 || len(w) > 5 {
			t.Fatalf("word %q outside EFF short list length bounds", w)
		}
		for _, r := range w {
			if r < 'a' || r > 'z' {
				t.Fatalf("word %q has a non-alpha rune", w)
			}
		}
	}
}

func TestNewFormat(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]+-\d{2}-[a-z]+$`)
	seen := map[string]bool{}
	for range 100 {
		p := New()
		if !re.MatchString(p) {
			t.Fatalf("phrase %q does not match word-NN-word", p)
		}
		seen[p] = true
	}
	// 100 draws from ~2^27 combos colliding down to ≤2 distinct values would
	// mean the RNG is broken, not unlucky.
	if len(seen) < 90 {
		t.Fatalf("only %d distinct phrases in 100 draws", len(seen))
	}
}

func TestSessionIDDeterministicAndCanonical(t *testing.T) {
	a := SessionID("lion-42-maple")
	b := SessionID("  Lion-42-MAPLE ")
	if a != b {
		t.Fatal("canonicalization: same phrase, different session IDs")
	}
	c := SessionID("lion-43-maple")
	if a == c {
		t.Fatal("different phrases produced the same session ID")
	}
	var zero [16]byte
	if a == zero {
		t.Fatal("session ID is all zeros")
	}
}

// Pin the derivation: the session ID is shared between independently built
// clients and relays — an accidental change to the hash context or
// canonicalization would break every in-flight share link. Changing this
// constant knowingly is a protocol version bump, not a refactor.
func TestSessionIDGolden(t *testing.T) {
	got := SessionID("lion-42-maple")
	if h := hex.EncodeToString(got[:]); h != "c5dd444266b890df48f7b8c1a7d3fe59" {
		t.Fatalf("session-ID derivation changed: %s", h)
	}
}
