// Package logging_test contains integration tests for the logging package
// against real Postgres via testcontainers.
//
// The internal worker_test.go covers the worker's contract against a fake
// Sink. This file covers the production path through *store.Store, which
// is what catches bugs in:
//
//   - pgx.Batch semantics
//   - nullable column handling (status_code, error_detail)
//   - the worker's flush-time context with real network I/O
//
// Tests here are slower (testcontainer boot ~10s) so they live in their
// own file. Skip with `go test -short ./internal/logging/...` if needed.
package logging_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LightAwesome/portcullis/internal/logging"
	"github.com/LightAwesome/portcullis/internal/testutil"
)

var infra *testutil.Infra

func TestMain(m *testing.M) {
	ctx := context.Background()

	var err error
	infra, err = testutil.StartInfra(ctx)
	if err != nil {
		panic("start infra: " + err.Error())
	}

	code := m.Run()

	infra.Stop(ctx)

	if code != 0 {
		// Propagate non-zero exit so `go test` sees the failure.
		panic("tests failed")
	}
}

// TestWorker_DrainsToRealPostgres is the headline P4.1 integration test.
//
// Property: entries Submitted before Stop end up in the request_logs
// table after Stop returns. Same property as TestWorker_StopDrainsPending
// in worker_test.go, but against the real *store.Store sink — exercising
// pgx.Batch, nullable column serialization, and the flush-time context
// with real network I/O.
//
// BatchTimeout is set very long (30s) and BatchSize larger than N so the
// only path that empties the channel is the drain branch in run()'s
// `case <-w.done`. A failure here means the drain path is broken,
// not the happy-path flush — that's checked by the fake-sink tests.
func TestWorker_DrainsToRealPostgres(t *testing.T) {
	infra.Reset(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	w := logging.NewWorker(infra.Store, logger, logging.Config{
		BufferSize:   512,
		BatchSize:    100,
		BatchTimeout: 30 * time.Second,
	})
	w.Start(context.Background())

	// request_logs.client_id is uuid NOT NULL but deliberately not a
	// foreign key (see migration 0001 — chronicle entries survive client
	// deletion). Any well-formed UUID string is accepted.
	const testClientID = "11111111-1111-1111-1111-111111111111"
	const N = 25

	baseTime := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < N; i++ {
		w.Submit(logging.LogEntry{
			ClientID:    testClientID,
			RoutePrefix: "httpbin",
			Method:      "GET",
			Path:        "/get",
			StatusCode:  200,
			LatencyMS:   42 + i,
			RateLimited: false,
			RequestedAt: baseTime.Add(time.Duration(i) * time.Millisecond),
		})
	}

	// Stop drains the channel and waits for the worker goroutine to exit.
	// After this returns, all 25 entries must be in Postgres.
	w.Stop()

	ctx := context.Background()
	pool := infra.Store.PoolForTesting()

	// Happy-path assertion: all N rows landed.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM request_logs WHERE route_prefix = $1`,
		"httpbin",
	).Scan(&count); err != nil {
		t.Fatalf("count request_logs: %v", err)
	}
	if count != N {
		t.Errorf("request_logs count: got %d, want %d", count, N)
	}

	// NULL-handling assertion: ErrorDetail was zero-valued for every
	// Submit, so InsertRequestLogs should have passed nil (→ SQL NULL)
	// rather than the empty string. If someone "simplifies" the nil
	// coercion in store.InsertRequestLogs, this catches it.
	var nullErrors int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM request_logs WHERE error_detail IS NULL`,
	).Scan(&nullErrors); err != nil {
		t.Fatalf("count null error_detail: %v", err)
	}
	if nullErrors != N {
		t.Errorf("rows with NULL error_detail: got %d, want %d", nullErrors, N)
	}
}
