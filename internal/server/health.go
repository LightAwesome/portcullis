package server

import (
	"context"
	"encoding/json"
	"github.com/LightAwesome/portcullis/internal/httpx"
	"net/http"
	"time"
)

// healthResponse is the JSON body returned by /health.
//
// "status" is the themed top-level summary; "redis" and "postgres" are
// per-dependency results (either "ok" or a brief error description).
type healthResponse struct {
	Status   string `json:"status"`
	Postgres string `json:"postgres"`
	Redis    string `json:"redis"`
}

// handleHealth returns the /health handler.
//
// Pings Postgres and Redis with a short timeout. Returns 200 if both are
// reachable, 503 if either fails. The response body always includes per-
// dependency status — even on 503 — so monitoring systems can parse it
// and know which dependency is failing without screen-scraping the body.
func handleHealth(deps *Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Bound the health check so it can't hang on a wedged dependency.
		// 2 seconds is generous for local pings; if either takes longer
		// than that, "unhealthy" is the right answer.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		body := healthResponse{
			Status:   "the gate stands",
			Postgres: "ok",
			Redis:    "ok",
		}
		statusCode := http.StatusOK

		if err := deps.Store.Ping(ctx); err != nil {
			// Store.Ping returns a single error covering both deps; we don't
			// currently distinguish which failed at the response level. Good
			// enough for now; we can split into per-dep pings later if the
			// joint error becomes confusing.
			httpx.LoggerFromContext(r.Context()).Warn("health check: store ping failed", "error", err)
			body.Status = "the gate falters"
			// Mark both as failed since we don't know which is the actual cause.
			// In Phase 3 we'll improve this by exposing per-dep ping methods.
			body.Postgres = "unknown"
			body.Redis = "unknown"
			statusCode = http.StatusServiceUnavailable
		}

		writeJSON(w, statusCode, body)
	}
}

// writeJSON writes v as the response body with the given status code.
//
// Lives in this file for now; will move to its own response helpers file
// when we have more handlers using it.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// At this point the response is partially written; we can't change
		// the status code. The best we can do is log. We'll wire a real
		// logger in P3.1; for now silent failure is acceptable for /health.
		_ = err
	}
}
