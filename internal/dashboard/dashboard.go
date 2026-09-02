// Package dashboard exposes read-only observability endpoints for the
// dashboard frontend. Unlike the admin API (which allows mutations), these
// endpoints are read-only: activity feed, current breaker states.
//
// Auth is intentionally light — a single shared token in
// PORTCULLIS_DASHBOARD_TOKEN. The endpoints reveal request metadata
// (client name, route prefix, status, latency) but no secrets. The
// token gate exists to prevent random internet scanners from watching
// activity, not to protect credentials — nothing here is credential-
// grade sensitive.
//
// Anyone with the token URL can view the dashboard. That is by design
// for a portfolio demo. Rotate the token to invalidate old shared links.
package dashboard

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/LightAwesome/portcullis/internal/breaker"
	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/store"
)

// Handler returns an HTTP handler bundle for the dashboard endpoints.
//
// Registers two routes on the given mux:
//
//	GET /dashboard/recent-requests?token=xxx&limit=20
//	GET /dashboard/breakers?token=xxx
//
// Both require the shared token. The token is checked in constant time
// to prevent timing-based enumeration (belt-and-suspenders — the tokens
// are 32 bytes of hex which are already unfeasible to brute-force).
func Register(mux Router, dashboardToken string, db *store.Store, breakers *breaker.Registry) {
	guard := tokenGuard(dashboardToken)

	mux.Get("/dashboard/recent-requests", guard(recentRequestsHandler(db)))
	mux.Get("/dashboard/breakers", guard(breakerStatesHandler(breakers)))
}

// Router is the small subset of chi.Router the dashboard needs.
// Defined here to keep this package independent of the chi import.
type Router interface {
	Get(pattern string, h http.HandlerFunc)
}

// tokenGuard returns middleware that requires the caller to present a
// token matching expected via ?token= or Authorization: Bearer.
func tokenGuard(expected string) func(http.HandlerFunc) http.HandlerFunc {
	expectedBytes := []byte(expected)
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Accept either ?token= (easier for dashboard fetch) or
			// Authorization: Bearer xxx (nicer for curl testing).
			provided := r.URL.Query().Get("token")
			if provided == "" {
				h := r.Header.Get("Authorization")
				if len(h) > 7 && h[:7] == "Bearer " {
					provided = h[7:]
				}
			}

			if expected == "" || subtle.ConstantTimeCompare([]byte(provided), expectedBytes) != 1 {
				httpx.WriteError(w, http.StatusUnauthorized,
					"dashboard_auth_failed", "the watchtower requires a token")
				return
			}
			next(w, r)
		}
	}
}

// RecentRequest is one row in the activity feed.
type RecentRequest struct {
	Timestamp   time.Time `json:"timestamp"`
	ClientName  string    `json:"client_name"`
	RoutePrefix string    `json:"route_prefix"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	StatusCode  int       `json:"status_code"`
	LatencyMs   int       `json:"latency_ms"`
}

// recentRequestsHandler returns the last N request_log entries, joined
// against clients so the response has human-readable client names
// rather than raw UUIDs.
//
// Default limit is 20, max 100. Ordered by timestamp desc so the feed
// is fresh-first.
func recentRequestsHandler(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if s := r.URL.Query().Get("limit"); s != "" {
			// Parse limit, clamp to [1, 100].
			var parsed int
			for _, c := range s {
				if c < '0' || c > '9' {
					parsed = 0
					break
				}
				parsed = parsed*10 + int(c-'0')
				if parsed > 100 {
					parsed = 100
					break
				}
			}
			if parsed > 0 {
				limit = parsed
			}
		}

		rows, err := db.RecentRequests(r.Context(), limit)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError,
				"chronicle_read_failed", "the chronicle could not be read")
			return
		}

		out := make([]RecentRequest, 0, len(rows))
		for _, row := range rows {
			out = append(out, RecentRequest{
				Timestamp:   row.Timestamp,
				ClientName:  row.ClientName,
				RoutePrefix: row.RoutePrefix,
				Method:      row.Method,
				Path:        row.Path,
				StatusCode:  row.StatusCode,
				LatencyMs:   row.LatencyMs,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"requests": out,
		})
	}
}

// BreakerState is one entry per registered route breaker.
type BreakerState struct {
	Route string `json:"route"`
	State string `json:"state"` // "closed" | "half-open" | "open"
}

// breakerStatesHandler enumerates registered breakers and their current
// state. Called by the dashboard on a slower cadence than the request
// feed; state changes are also visible via the state gauge in metrics.
func breakerStatesHandler(breakers *breaker.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := make([]BreakerState, 0)
		breakers.ForEach(func(name string, b *breaker.Breaker) {
			out = append(out, BreakerState{
				Route: name,
				State: b.State().String(),
			})
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"breakers": out,
		})
	}
}
