// Package metrics defines all Prometheus metrics exported by Portcullis.
//
// Metrics are registered in init() against the default Prometheus registry.
// Middlewares and the logging worker call the increment helpers here;
// the /metrics endpoint (P3.6) serves the registry over HTTP.
//
// Cardinality discipline: label values must be bounded — method, status,
// route prefix, client name. Never label by request ID, URL path, IP,
// or user agent.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Naming convention: portcullis_<subsystem>_<name>_<unit>.
//
// _total suffix on counters per Prometheus convention.
// _seconds suffix on duration histograms per OpenMetrics base unit.

var (
	requestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "portcullis_requests_total",
			Help: "Total HTTP requests received, labeled by method, route, status, and client.",
		},
		[]string{"method", "route", "status", "client"},
	)

	requestDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "portcullis_request_duration_seconds",
			Help:    "HTTP request latency in seconds, labeled by route.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"route"},
	)

	rateLimitedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "portcullis_rate_limited_total",
			Help: "Requests denied by the rate limiter, labeled by client and route.",
		},
		[]string{"client", "route"},
	)

	upstreamErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "portcullis_upstream_errors_total",
			Help: "Upstream request failures, labeled by route and category.",
		},
		[]string{"route", "category"},
	)

	logsDroppedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "portcullis_logs_dropped_total",
			Help: "Log entries dropped because the buffer was full.",
		},
	)
)

// RecordRequest increments the request and duration metrics.
//
// Called from the metrics middleware. Label values are normalized: empty
// strings become "unknown" so the metric still produces a valid time
// series rather than being silently filtered.
func RecordRequest(method, route, status, client string, durationSeconds float64) {
	method = orUnknown(method)
	route = orUnknown(route)
	status = orUnknown(status)
	client = orUnknown(client)

	requestsTotal.WithLabelValues(method, route, status, client).Inc()
	requestDurationSeconds.WithLabelValues(route).Observe(durationSeconds)
}

// RecordRateLimited increments the rate-limited counter for (client, route).
//
// Called from the chronicle middleware (or directly from the rate-limit
// middleware — either is fine; the middleware that owns the rate-limit
// decision is the natural caller, which is the rate-limit middleware).
func RecordRateLimited(client, route string) {
	rateLimitedTotal.WithLabelValues(orUnknown(client), orUnknown(route)).Inc()
}

// RecordUpstreamError increments the upstream-error counter for (route, category).
//
// Categories: "unreachable", "timeout", "inactive", "other".
func RecordUpstreamError(route, category string) {
	upstreamErrorsTotal.WithLabelValues(orUnknown(route), orUnknown(category)).Inc()
}

// RecordLogDropped increments the dropped-logs counter by one.
//
// Called from the logging worker's Submit path when the channel buffer
// is full.
func RecordLogDropped() {
	logsDroppedTotal.Inc()
}

// orUnknown returns "unknown" for empty strings.
//
// Prometheus rejects empty label values silently in some versions and
// produces useless `{label=""}` series in others. Forcing a sentinel
// value keeps queries clean.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
