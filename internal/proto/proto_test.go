package proto

import (
	"encoding/hex"
	"testing"

	"github.com/richardwooding/parley/phrase"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
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

// Pin the deployed role bytes: they ride the encrypted handshake and the ctl
// roster as raw uint8s, so every kibitz build must agree on them. The
// equality with parley's generic constants trips if a parley upgrade ever
// renumbers its defaults out from under us.
func TestRoleBytesPinned(t *testing.T) {
	if uint8(RolePlayer) != 2 || uint8(RoleSpectator) != 3 {
		t.Fatalf("role bytes changed: player=%d spectator=%d", RolePlayer, RoleSpectator)
	}
	if RolePlayer != session.RoleMember || RoleSpectator != session.RoleObserver {
		t.Fatal("parley renumbered its default roles")
	}
}

// RolePolicy must reproduce the legacy in-library assignment exactly — old
// and new kibitz hosts must seat the same join sequence identically.
func TestRolePolicyLegacy(t *testing.T) {
	none := map[wire.ParticipantID]session.Role{}
	hasPlayer := map[wire.ParticipantID]session.Role{5: RolePlayer}
	hasSpec := map[wire.ParticipantID]session.Role{5: RoleSpectator}
	cases := []struct {
		observer bool
		assigned map[wire.ParticipantID]session.Role
		want     session.Role
	}{
		{true, none, RoleSpectator},
		{false, none, RolePlayer},
		{false, hasPlayer, RoleSpectator},
		{true, hasPlayer, RoleSpectator},
		{false, hasSpec, RolePlayer},
	}
	for i, tc := range cases {
		if got := RolePolicy(9, tc.observer, tc.assigned); got != tc.want {
			t.Fatalf("case %d: got %d want %d", i, got, tc.want)
		}
	}
}
