package store

import (
	"context"
	"time"
)

// RecentRequestRow is one row from the recent-requests query. Includes
// the client's human-readable name via join, and everything the
// dashboard needs to render one activity feed entry.
type RecentRequestRow struct {
	Timestamp   time.Time
	ClientName  string
	RoutePrefix string
	Method      string
	Path        string
	StatusCode  int
	LatencyMs   int
}

// RecentRequests returns the last `limit` entries from request_logs,
// joined against clients to include the client's name.
//
// Ordered by timestamp desc — freshest first. Callers should typically
// pass limit=20 for feed display; the query itself imposes no cap.
//
// The join is a plain LEFT JOIN so entries whose client has been
// deleted still show up (with client_name = "").
func (s *Store) RecentRequests(ctx context.Context, limit int) ([]RecentRequestRow, error) {
	const q = `
    SELECT
        rl.requested_at,                       -- was: rl.created_at
        COALESCE(c.name, '') AS client_name,
        rl.route_prefix,
        rl.method,
        rl.path,
        rl.status_code,
        rl.latency_ms
    FROM request_logs rl
    LEFT JOIN clients c ON c.id = rl.client_id
    ORDER BY rl.requested_at DESC              -- was: rl.created_at
    LIMIT $1
`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RecentRequestRow, 0, limit)
	for rows.Next() {
		var r RecentRequestRow
		if err := rows.Scan(
			&r.Timestamp,
			&r.ClientName,
			&r.RoutePrefix,
			&r.Method,
			&r.Path,
			&r.StatusCode,
			&r.LatencyMs,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
