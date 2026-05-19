// Package logging implements the gateway's async request-log pipeline.
//
// The pipeline decouples request handling from log persistence:
//
//   - Request handlers submit LogEntry values to a Worker via Submit, which
//     uses select-default to push to a buffered channel without blocking.
//   - The Worker drains the channel in batches (50 entries or 500ms,
//     whichever first) and writes them to Postgres via pgx.Batch.
//   - Submit failures (channel full) increment the dropped counter; the
//     request still returns promptly.
//   - On Stop, the Worker flushes whatever's pending before exiting,
//     preserving log entries across graceful shutdown.
package logging

import "time"

// LogEntry is a single request's audit record. Mirrors the schema of
// the request_logs Postgres table; conversion happens at the store
// boundary.
//
// ErrorDetail is empty unless the request failed at the gateway level
// (auth, upstream unreachable, etc.). For successful proxy responses
// it stays empty.

type LogEntry struct {
	ClientID    string
	RoutePrefix string
	Method      string
	Path        string
	StatusCode  int
	LatencyMS   int
	RateLimited bool
	ErrorDetail string
	RequestedAt time.Time
}
