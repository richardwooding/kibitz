package proto

import (
	"encoding/hex"
	"testing"

	"github.com/richardwooding/parley/phrase"
)

// Pin the deployed derivation: parley under kibitz's label must produce the
// exact session ID every deployed client derives from this phrase. If this
// fails, a parley upgrade changed the hash context, canonicalization, or
// label plumbing — shipping it would strand new builds in different relay
// sessions from old ones. Changing this constant knowingly is a protocol
// version bump, not a refactor.
func TestSessionIDGolden(t *testing.T) {
	got := phrase.SessionID(Label, "lion-42-maple")
	if h := hex.EncodeToString(got[:]); h != "c5dd444266b890df48f7b8c1a7d3fe59" {
		t.Fatalf("deployed session-ID derivation changed: %s", h)
	}
}
