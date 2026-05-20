package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/metrics"
)

// metricsMiddleware records each request's outcome to Prometheus metrics.
//
// Runs outermost so it observes every response, including auth failures
// (401) and not-found routes (404) that short-circuit before reaching
// the proxy or rate-limit middlewares.
//
// Label values:
//   - method: HTTP method (always present)
//   - route:  matched route prefix from chi.URLParam, or "_admin" / "_health" / "_other" for non-proxy
//   - status: integer status code as string
//   - client: authenticated client name, or "unauthenticated"
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusCapturingWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		ctx, labels := httpx.WithMetricLabels(r.Context())
		r = r.WithContext(ctx)

		next.ServeHTTP(ww, r)

		client := labels.Client
		if client == "" {
			client = "unauthenticated"
		}

		route := labels.Route
		if route == "" {
			route = fallbackRouteLabel(r.URL.Path)
		}

		metrics.RecordRequest(
			r.Method,
			route,
			strconv.Itoa(ww.statusCode),
			client,
			time.Since(start).Seconds(),
		)
	})
}

func fallbackRouteLabel(path string) string {
	switch {
	case path == "/health":
		return "_health"
	case path == "/metrics":
		return "_metrics"
	case path == "/admin" || strings.HasPrefix(path, "/admin/"):
		return "_admin"
	case path == "/proxy" || strings.HasPrefix(path, "/proxy/"):
		return "_proxy_unmatched"
	default:
		return "_other"
	}
}

// // metricRouteLabel returns a stable label for the route, suitable for
// // Prometheus.
// //
// // For /proxy/{prefix}/..., returns the prefix (e.g., "httpbin").
// // For other routes, returns a coarse category to keep cardinality low:
// //   - /admin/*   -> "_admin"
// //   - /health    -> "_health"
// //   - /metrics   -> "_metrics"
// //   - anything else -> "_other"
// //
// // The underscore prefix on non-proxy categories avoids collisions with
// // real route prefixes (which validate as [a-z0-9-]+ — no underscores).
// func metricRouteLabel(r *http.Request) string {
// 	if prefix := chi.URLParam(r, "prefix"); prefix != "" {
// 		return prefix
// 	}
// 	path := r.URL.Path
// 	switch {
// 	case path == "/health":
// 		return "_health"
// 	case path == "/metrics":
// 		return "_metrics"
// 	case len(path) >= 6 && path[:6] == "/admin":
// 		return "_admin"
// 	case len(path) >= 7 && path[:7] == "/proxy/":
// 		// /proxy/ but no prefix matched — unusual. Tag separately.
// 		return "_proxy_unmatched"
// 	default:
// 		return "_other"
// 	}
// }
//
// // metricClientLabel returns the client name for the authenticated client,
// // or "unauthenticated" if no client is in context.
// //
// // Uses the name (not UUID) for human-readable Grafana queries. Cardinality
// // is bounded by the number of registered clients — well within Prometheus
// // best practices.
// func metricClientLabel(r *http.Request) string {
// 	c, ok := auth.ClientFromContext(r.Context())
// 	if !ok {
// 		return "unauthenticated"
// 	}
// 	return c.Name
// }
