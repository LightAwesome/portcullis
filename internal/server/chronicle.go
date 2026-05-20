package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LightAwesome/portcullis/internal/auth"
	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/logging"
	"github.com/LightAwesome/portcullis/internal/store"
)

// chronicleMiddleware records every proxied request to the async
// log worker.
//
// Mounted only on /proxy/{prefix}/* — internal endpoints (/health,
// /admin, /metrics) are not recorded. The chronicle is the audit
// trail of *proxy traffic*, not gateway operations.
//
// Must run after auth (needs client_id from context) and rate-limit
// (needs to observe the final status). Runs before the proxy handler
// in the wrap-and-measure pattern.
func chronicleMiddleware(worker *logging.Worker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			client, ok := auth.ClientFromContext(r.Context())
			if !ok {
				// Defensive: chronicle is mounted only inside the auth-
				// gated route group, so this branch is unreachable in
				// correct configuration. Warn and pass through if a
				// future refactor mounts it elsewhere by mistake.
				httpx.LoggerFromContext(r.Context()).Warn("chronicle: no client in context")
				next.ServeHTTP(w, r)
				return
			}

			prefix := chi.URLParam(r, "prefix")
			if prefix == "" {
				httpx.LoggerFromContext(r.Context()).Warn("chronicle: no prefix in url params")
				next.ServeHTTP(w, r)
				return
			}

			clientID := clientIDString(client)
			if clientID == "" {
				httpx.LoggerFromContext(r.Context()).Warn("chronicle: client UUID conversion failed")
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			ww := &statusCapturingWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(ww, r)

			entry := logging.LogEntry{
				ClientID:    clientID,
				RoutePrefix: prefix,
				Method:      r.Method,
				Path:        r.URL.Path,
				StatusCode:  ww.statusCode,
				LatencyMS:   int(time.Since(start).Milliseconds()),
				RateLimited: ww.statusCode == http.StatusTooManyRequests,
				ErrorDetail: errorDetailFromStatus(ww.statusCode),
				RequestedAt: start,
			}
			worker.Submit(entry)
		})
	}
}

// errorDetailFromStatus returns a human-readable error category for
// non-success status codes, "" for success.
//
// Coarse-grained: distinguishes upstream errors from gateway errors
// from rate-limiting, but doesn't reach into the response body for
// machine codes. Refined post-Phase-5 if call sites need precision.
func errorDetailFromStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return ""
	case status == http.StatusUnauthorized:
		return "auth_failed"
	case status == http.StatusForbidden:
		return "forbidden"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status == http.StatusBadGateway:
		return "upstream_unreachable"
	case status == http.StatusServiceUnavailable:
		return "upstream_inactive"
	case status == http.StatusGatewayTimeout:
		return "upstream_timeout"
	case status >= 500:
		return "internal_error"
	default:
		return "client_error"
	}
}

// clientIDString extracts the canonical string form of a client UUID.
// Duplicates the helper in ratelimit/middleware.go intentionally —
// the alternative is a shared helper, which is a sub-ticket worth
// of refactoring for one extra call site. Revisit in Phase 6 when
// the admin CLI handlers also need this.
func clientIDString(c *store.Client) string {
	v, err := c.ID.Value()
	if err != nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
