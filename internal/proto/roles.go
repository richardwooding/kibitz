package proto

import (
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
)

// Game roles carried inside the encrypted handshake and announced on the ctl
// roster as raw uint8s. The byte values are deployed protocol — 2 and 3 are
// what every shipped kibitz client sends and expects (they were
// session.RolePlayer / session.RoleSpectator before parley made roles
// pluggable). Renumbering is a protocol version bump.
const (
	RolePlayer    session.Role = 2
	RoleSpectator session.Role = 3
)

// RolePolicy reproduces kibitz's deployed seat assignment byte-for-byte: a
// joiner that asked to watch is always a spectator (it never takes the open
// player seat); otherwise the first non-watcher to key becomes the player
// and everyone after is a spectator. Known quirk preserved from the old
// in-library policy: a host promoted by migration starts with an empty
// assigned map, so the next non-watcher to join may take the player seat
// even when a player survived the migration.
func RolePolicy(_ wire.ParticipantID, observer bool, assigned map[wire.ParticipantID]session.Role) session.Role {
	if observer {
		return RoleSpectator
	}
	for _, r := range assigned {
		if r == RolePlayer {
			return RoleSpectator
		}
	}
	return RolePlayer
}

// Options is the bundle every session.Host / session.Join call must pass:
// kibitz's protocol label plus its role policy. Join sites append
// session.WithObserver() for a spectator join. Returns a fresh slice, so
// callers may append.
func Options() []session.Option {
	return []session.Option{session.WithProtocol(Label), session.WithRolePolicy(RolePolicy)}
}
