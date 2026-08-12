// Package service is a compatibility shim: the layered-service mux that
// used to live here was extracted into github.com/richardwooding/parley/service
// (kibitz was its first consumer). Everything below is a type alias or a
// thin wrapper, so the 40+ importers across the game services, solo, the
// WASM bridge, and the integration tests compile unchanged. New code may
// import parley/service directly.
//
// One nuance guards the aliasing: parley's DefaultSuccessorPolicy prefers
// RoleHost/RoleMember survivors, which matches kibitz's deployed election
// (host/player) only because proto.RolePlayer occupies the same byte as
// session.RoleMember — pinned by internal/proto's tests. If kibitz ever
// renumbers roles it must pass psvc.WithSuccessor explicitly.
package service

import psvc "github.com/richardwooding/parley/service"

type (
	Sender         = psvc.Sender
	Context        = psvc.Context
	Service        = psvc.Service
	MemberObserver = psvc.MemberObserver
	Promotable     = psvc.Promotable
	Base           = psvc.Base
	Conn           = psvc.Conn
	Mux            = psvc.Mux
	Desync         = psvc.Desync
	ServiceError   = psvc.ServiceError
	SessionEvent   = psvc.SessionEvent
	Promoted       = psvc.Promoted
	Promoter       = psvc.Promoter
	Roster         = psvc.Roster
	ServiceInfo    = psvc.ServiceInfo
)

// CtlID is the reserved control service identifier.
const CtlID = psvc.CtlID

// NewMux preserves kibitz's historical variadic-services signature.
func NewMux(c Conn, svcs ...Service) *Mux {
	return psvc.NewMux(c, psvc.WithServices(svcs...))
}
