// Package dashboard is a compatibility shim over
// github.com/richardwooding/parley/dashboard, where the GitHub-OAuth-gated
// admin dashboard was extracted (kibitz was its first consumer). cmd/kibitz
// passes AppName "kibitz" so the page title and session cookie are unchanged
// from the pre-extraction build.
package dashboard

import (
	"net/http"

	pdash "github.com/richardwooding/parley/dashboard"
)

type (
	// Config is the dashboard's runtime configuration.
	Config = pdash.Config
	// Dashboard serves the admin + auth routes; Register mounts them.
	Dashboard = pdash.Dashboard
	// StatsSource yields a blind-safe relay snapshot; *relay.Server implements it.
	StatsSource = pdash.StatsSource
	// PeerLister returns the base URLs of the other relay nodes (this one
	// excluded) for cluster-wide stats aggregation.
	PeerLister = pdash.PeerLister
)

// New builds a Dashboard wired to production GitHub endpoints.
func New(cfg Config, src StatsSource) *Dashboard { return pdash.New(cfg, src) }

// NewAggregator wraps a local StatsSource so Stats() merges this node's shard
// with every peer's — the cluster-wide view under multi-node.
func NewAggregator(local StatsSource, peers PeerLister, token []byte) StatsSource {
	return pdash.NewAggregator(local, peers, token)
}

// InternalStatsPath is where each node serves its raw Stats for peers to scrape.
func InternalStatsPath() string { return pdash.InternalStatsPath() }

// InternalStatsHandler serves this node's raw Stats, gated by a shared token.
func InternalStatsHandler(local StatsSource, token []byte) http.Handler {
	return pdash.InternalStatsHandler(local, token)
}
