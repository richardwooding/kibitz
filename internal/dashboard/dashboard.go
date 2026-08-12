// Package dashboard is a compatibility shim over
// github.com/richardwooding/parley/dashboard, where the GitHub-OAuth-gated
// admin dashboard was extracted (kibitz was its first consumer). cmd/kibitz
// passes AppName "kibitz" so the page title and session cookie are unchanged
// from the pre-extraction build.
package dashboard

import pdash "github.com/richardwooding/parley/dashboard"

type (
	// Config is the dashboard's runtime configuration.
	Config = pdash.Config
	// Dashboard serves the admin + auth routes; Register mounts them.
	Dashboard = pdash.Dashboard
	// StatsSource yields a blind-safe relay snapshot; *relay.Server implements it.
	StatsSource = pdash.StatsSource
)

// New builds a Dashboard wired to production GitHub endpoints.
func New(cfg Config, src StatsSource) *Dashboard { return pdash.New(cfg, src) }
