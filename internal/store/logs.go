package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/LightAwesome/portcullis/internal/logging"
)

// InsertRequestLogs writes a batch of log entries to the request_logs table.
//
// Uses pgx.Batch to issue all INSERTs as one network round-trip. On any
// per-statement error, returns the first such error (caller logs it and
// moves on — log inserts are best-effort, not transactional).
//
// An empty batch is a no-op and returns nil.
func (s *Store) InsertRequestLogs(ctx context.Context, entries []logging.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	const q = `
		INSERT INTO request_logs (
			client_id, route_prefix, method, path,
			status_code, latency_ms, rate_limited, error_detail, requested_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	batch := &pgx.Batch{}
	for _, e := range entries {
		// status_code is nullable (gateway-rejected requests have no upstream status).
		// Pass NULL for status 0; otherwise the integer value.
		var statusCode any
		if e.StatusCode > 0 {
			statusCode = e.StatusCode
		}

		var errorDetail any
		if e.ErrorDetail != "" {
			errorDetail = e.ErrorDetail
		}

		batch.Queue(q,
			e.ClientID, e.RoutePrefix, e.Method, e.Path,
			statusCode, e.LatencyMS, e.RateLimited, errorDetail, e.RequestedAt,
		)
	}

	results := s.pool.SendBatch(ctx, batch)
	defer func() {
		_ = results.Close()
	}()
	for i := 0; i < len(entries); i++ {
		if _, err := results.Exec(); err != nil {
			// First failure aborts the rest; pgx will discard remaining results.
			// We surface the first error since log inserts are not transactional.
			if errors.Is(err, pgx.ErrTxClosed) {
				continue
			}
			return fmt.Errorf("insert request_log[%d]: %w", i, err)
		}
	}
	return nil
}
