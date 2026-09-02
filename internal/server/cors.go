package server

import (
	"net/http"
	"strings"
)

// corsMiddleware allows the dashboard frontend to fetch dashboard endpoints
// from a different origin (e.g. dashboard.touseef.pages.dev).
//
// Design:
//   - Only allowed origins pass. No wildcard. The list is comma-separated
//     in the config value (e.g. PORTCULLIS_ALLOWED_ORIGINS).
//   - Only dashboard endpoints get CORS headers. Admin and proxy paths
//     are same-origin from your own tools (curl, seed script) so they
//     don't need CORS.
//   - Preflight OPTIONS responses are handled here; the guarded handler
//     doesn't need to know about CORS.
//
// Config: PORTCULLIS_ALLOWED_ORIGINS="https://dashboard.example.com,http://localhost:5173"
// The localhost entry lets `npm run dev` work against a real gateway.
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	// Normalize once — strip trailing slashes, build a map for O(1) lookup.
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o != "" {
			allowed[o] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only intercept dashboard endpoints. Everything else runs through
			// unchanged — proxy traffic, admin API, health, metrics.
			if !strings.HasPrefix(r.URL.Path, "/dashboard/") {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.Header().Set("Access-Control-Max-Age", "3600")
			}

			// Preflight — respond immediately, don't forward to the handler.
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
