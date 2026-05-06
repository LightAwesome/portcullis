package server

import "github.com/go-chi/chi/v5"

// addRoutes registers all endpoints the gateway exposes.
//
// This is the single source of truth for "what URLs does this server
// serve?" — grep here, not anywhere else, to find a route.
//
// Route layout:
//
//	GET  /health         — system health, used by Docker and uptime monitors
//
// Future routes (added in later phases):
//
//	GET  /metrics        — Prometheus scrape endpoint            (P3.6)
//	ALL  /proxy/{p}/*    — authenticated, rate-limited proxy     (P1.13–P2.4)
//	/admin/*             — admin API, gated by X-Admin-Key       (P1.15, P4)
//	/dashboard/*         — embedded React SPA                    (P8)
func addRoutes(mux chi.Router, deps *Dependencies) {
	mux.Get("/health", handleHealth(deps))
}
