package logging

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Sink is what a Worker writes to. Implemented by *store.Store; the
// interface lets us fake it in tests without spinning up Postgres.
type Sink interface {
	InsertRequestLogs(ctx context.Context, entries []LogEntry) error
}

// Worker consumes LogEntry values from a buffered channel and writes
// them to a Sink in batches.
//
// Construct with NewWorker, then call Start. Submit pushes entries; on
// shutdown, call Stop and wait — Stop blocks until the worker has flushed
// pending entries and exited.
type Worker struct {
	sink         Sink
	logger       *slog.Logger
	batchSize    int
	batchTimeout time.Duration

	ch       chan LogEntry
	done     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once

	dropped  atomic.Int64
	enqueued atomic.Int64
}

// Config bundles knobs for the Worker. Sensible defaults if zero.
type Config struct {
	BufferSize   int           // channel capacity; default 512
	BatchSize    int           // max entries per flush; default 50
	BatchTimeout time.Duration // flush interval; default 500ms
}

// NewWorker constructs a Worker but does not start it. Call Start to begin.
func NewWorker(sink Sink, logger *slog.Logger, cfg Config) *Worker {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 512
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = 500 * time.Millisecond
	}

	return &Worker{
		sink:         sink,
		logger:       logger,
		batchSize:    cfg.BatchSize,
		batchTimeout: cfg.BatchTimeout,
		ch:           make(chan LogEntry, cfg.BufferSize),
		done:         make(chan struct{}),
	}
}

// Start launches the worker loop in its own goroutine.
//
// Start should be called once. It registers the goroutine with the
// WaitGroup before launching it, so Stop cannot return before the worker
// has actually started.
func (w *Worker) Start(ctx context.Context) {
	w.wg.Add(1)

	go func() {
		defer w.wg.Done()
		w.run(ctx)
	}()
}

// Run is kept for compatibility with older call sites.
//
// Prefer Start(ctx). Do not call Run with the go keyword anymore; Start
// owns the goroutine lifecycle safely.
func (w *Worker) Run(ctx context.Context) {
	w.Start(ctx)
}

// Submit pushes an entry onto the worker's channel.
//
// Genuinely non-blocking: if the channel is full, the entry is dropped
// and the dropped counter increments. The caller never waits.
//
// Safe to call from many goroutines concurrently.
func (w *Worker) Submit(entry LogEntry) {
	select {
	case w.ch <- entry:
		w.enqueued.Add(1)
	default:
		w.dropped.Add(1)
	}
}

// run consumes entries until Stop is called or ctx is cancelled, then
// drains pending entries and returns.
func (w *Worker) run(ctx context.Context) {
	batch := make([]LogEntry, 0, w.batchSize)
	timer := time.NewTimer(w.batchTimeout)
	defer timer.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}

		// Use a bounded background context for the insert so even
		// shutdown-time flushes have time to complete.
		flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := w.sink.InsertRequestLogs(flushCtx, batch); err != nil {
			w.logger.Warn("log batch insert failed",
				"error", err,
				"batch_size", len(batch),
			)
		}

		batch = batch[:0]

		if !timer.Stop() {
			// Drain the channel if it had already fired.
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(w.batchTimeout)
	}

	for {
		select {
		case entry, ok := <-w.ch:
			if !ok {
				// Channel closed externally. This package does not close
				// w.ch, but handle it defensively.
				flush()
				return
			}

			batch = append(batch, entry)
			if len(batch) >= w.batchSize {
				flush()
			}

		case <-timer.C:
			flush()

		case <-w.done:
			// Drain whatever is already queued before exiting.
			for {
				select {
				case entry := <-w.ch:
					batch = append(batch, entry)
					if len(batch) >= w.batchSize {
						flush()
					}

				default:
					flush()
					return
				}
			}

		case <-ctx.Done():
			flush()
			return
		}
	}
}

// Stop signals the worker to drain and exit, then blocks until it has.
//
// Idempotent: calling Stop multiple times is safe. Must be called after
// the HTTP server has stopped accepting new requests, since in-flight
// requests can still call Submit until they complete.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
		close(w.done)
	})

	w.wg.Wait()
}

// Stats returns the current enqueued and dropped counts.
//
// Used by the /metrics endpoint (P3.5) and useful for debugging.
type Stats struct {
	Enqueued int64
	Dropped  int64
}

func (w *Worker) Stats() Stats {
	return Stats{
		Enqueued: w.enqueued.Load(),
		Dropped:  w.dropped.Load(),
	}
}
