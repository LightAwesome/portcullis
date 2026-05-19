package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSink is a Sink that records what was inserted.
type fakeSink struct {
	mu       sync.Mutex
	batches  [][]LogEntry
	calls    atomic.Int64
	failOnce atomic.Bool
}

func (f *fakeSink) InsertRequestLogs(ctx context.Context, entries []LogEntry) error {
	f.calls.Add(1)

	if f.failOnce.CompareAndSwap(true, false) {
		return errors.New("simulated insert failure")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	batch := make([]LogEntry, len(entries))
	copy(batch, entries)
	f.batches = append(f.batches, batch)

	return nil
}

func (f *fakeSink) totalEntries() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0
	for _, b := range f.batches {
		n += len(b)
	}

	return n
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("condition not met within %s", timeout)
}

func TestWorker_BasicFlow(t *testing.T) {
	sink := &fakeSink{}

	w := NewWorker(sink, newTestLogger(), Config{
		BufferSize:   100,
		BatchSize:    3,
		BatchTimeout: 50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx)
	defer w.Stop()

	// Submit 6 entries; should produce 2 full batches of 3.
	for i := 0; i < 6; i++ {
		w.Submit(LogEntry{ClientID: "c", Method: "GET", Path: "/x"})
	}

	waitUntil(t, 500*time.Millisecond, func() bool {
		return sink.totalEntries() == 6
	})

	if got := sink.totalEntries(); got != 6 {
		t.Errorf("total entries: got %d, want 6", got)
	}
}

func TestWorker_TimeoutFlush(t *testing.T) {
	sink := &fakeSink{}

	w := NewWorker(sink, newTestLogger(), Config{
		BufferSize:   100,
		BatchSize:    100,
		BatchTimeout: 50 * time.Millisecond,
	})

	w.Start(context.Background())
	defer w.Stop()

	// Submit fewer than batchSize; should be flushed by the timeout.
	w.Submit(LogEntry{ClientID: "c", Method: "GET", Path: "/x"})

	waitUntil(t, 500*time.Millisecond, func() bool {
		return sink.totalEntries() == 1
	})

	if got := sink.totalEntries(); got != 1 {
		t.Errorf("entry not flushed by timeout; got %d", got)
	}
}

func TestWorker_NonBlockingSubmit(t *testing.T) {
	// Use a sink that blocks forever on insert.
	sink := &blockingSink{blocked: make(chan struct{})}

	w := NewWorker(sink, newTestLogger(), Config{
		BufferSize:   3,
		BatchSize:    1,
		BatchTimeout: 10 * time.Millisecond,
	})

	w.Start(context.Background())

	defer func() {
		close(sink.blocked)
		w.Stop()
	}()

	// Submit way more than the buffer can hold. None should block.
	const N = 100

	start := time.Now()
	for i := 0; i < N; i++ {
		w.Submit(LogEntry{ClientID: "c", Method: "GET", Path: "/x"})
	}
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("submit blocked: %d submissions took %s, expected sub-50ms", N, elapsed)
	}

	stats := w.Stats()

	if stats.Dropped == 0 {
		t.Errorf("expected drops with blocked sink, got %d", stats.Dropped)
	}

	if stats.Enqueued+stats.Dropped != int64(N) {
		t.Errorf(
			"counters: enqueued %d + dropped %d != submitted %d",
			stats.Enqueued,
			stats.Dropped,
			N,
		)
	}
}

// blockingSink blocks on InsertRequestLogs until its channel is closed.
type blockingSink struct {
	blocked chan struct{}
}

func (b *blockingSink) InsertRequestLogs(ctx context.Context, entries []LogEntry) error {
	<-b.blocked
	return nil
}

func TestWorker_StopDrainsPending(t *testing.T) {
	sink := &fakeSink{}

	w := NewWorker(sink, newTestLogger(), Config{
		BufferSize:   100,
		BatchSize:    100,
		BatchTimeout: 10 * time.Second,
	})

	w.Start(context.Background())

	for i := 0; i < 25; i++ {
		w.Submit(LogEntry{ClientID: "c", Method: "GET", Path: "/x"})
	}

	w.Stop()

	if got := sink.totalEntries(); got != 25 {
		t.Errorf("pending entries lost on Stop: got %d, want 25", got)
	}
}

func TestWorker_StopIsIdempotent(t *testing.T) {
	w := NewWorker(&fakeSink{}, newTestLogger(), Config{})

	w.Start(context.Background())

	w.Stop()
	w.Stop()
	w.Stop()
}

func TestWorker_StopToleratesInsertErrors(t *testing.T) {
	sink := &fakeSink{}
	sink.failOnce.Store(true)

	w := NewWorker(sink, newTestLogger(), Config{
		BufferSize:   100,
		BatchSize:    2,
		BatchTimeout: 10 * time.Millisecond,
	})

	w.Start(context.Background())
	defer w.Stop()

	// Submit; first batch insert fails. The worker should log and continue.
	// Subsequent batches should still succeed.
	for i := 0; i < 4; i++ {
		w.Submit(LogEntry{ClientID: "c", Method: "GET", Path: "/x"})
	}

	waitUntil(t, 500*time.Millisecond, func() bool {
		// First batch of 2 fails and is dropped.
		// Second batch of 2 should succeed.
		return sink.totalEntries() == 2
	})

	if got := sink.totalEntries(); got != 2 {
		t.Errorf("expected 2 entries after failure-then-success; got %d", got)
	}
}
