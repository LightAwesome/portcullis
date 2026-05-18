package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SiegeConfig parameterises a load test. Constructed from CLI flags or
// programmatically by tests.
type SiegeConfig struct {
	URL        string        // full URL to hit, e.g. http://localhost:8080/proxy/httpbin/get
	Method     string        // GET, POST, etc.
	GatewayKey string        // value for X-Gateway-Key header
	Concurrent int           // worker count
	Total      int           // total requests to fire
	Timeout    time.Duration // per-request timeout
}

// SiegeResult is the aggregated outcome of a single siege run.
type SiegeResult struct {
	TotalRequests int
	Duration      time.Duration
	StatusCounts  map[int]int     // status code -> count
	Errors        int             // requests that failed before getting a status
	Latencies     []time.Duration // sorted ascending; used for percentile calcs
}

// runSiege executes a siege against the configured target.
//
// Returns the aggregated results. Progress is written to progressOut (or
// silenced if progressOut is nil) during execution; that allows callers
// to control whether the progress bar shows in tests vs. real usage.
//
// The context's cancellation aborts in-flight workers — useful for
// Ctrl-C during a long siege.
func runSiege(ctx context.Context, cfg SiegeConfig, progressOut io.Writer) (*SiegeResult, error) {
	if cfg.Concurrent < 1 {
		return nil, fmt.Errorf("concurrent must be >= 1")
	}
	if cfg.Total < 1 {
		return nil, fmt.Errorf("total must be >= 1")
	}
	if cfg.GatewayKey == "" {
		return nil, fmt.Errorf("gateway key required")
	}

	// HTTP client shared across all workers. Configured for the siege's
	// throughput profile — many concurrent connections to one target.
	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.Concurrent,
			MaxIdleConnsPerHost: cfg.Concurrent,
			MaxConnsPerHost:     cfg.Concurrent,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	type workerResult struct {
		statusCode int
		duration   time.Duration
		err        error
	}

	jobs := make(chan int, cfg.Total)
	results := make(chan workerResult, cfg.Total)
	var wg sync.WaitGroup

	// Launch workers.
	for w := 0; w < cfg.Concurrent; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				select {
				case <-ctx.Done():
					results <- workerResult{err: ctx.Err()}
					continue
				default:
				}

				start := time.Now()
				req, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, nil)
				if err != nil {
					results <- workerResult{err: err, duration: time.Since(start)}
					continue
				}
				req.Header.Set("X-Gateway-Key", cfg.GatewayKey)

				resp, err := client.Do(req)
				duration := time.Since(start)
				if err != nil {
					results <- workerResult{err: err, duration: duration}
					continue
				}
				// Drain and close so the connection can be reused since igt can only be reused after reading it.
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()

				results <- workerResult{statusCode: resp.StatusCode, duration: duration}
			}
		}()
	}

	// Enqueue work.
	go func() {
		for i := 0; i < cfg.Total; i++ {
			jobs <- i
		}
		close(jobs)
	}()

	// Collector + progress display.
	var completed atomic.Int64
	progressDone := make(chan struct{})
	if progressOut != nil {
		go showProgress(ctx, progressOut, &completed, cfg.Total, progressDone)
	} else {
		close(progressDone)
	}

	overallStart := time.Now()

	out := &SiegeResult{
		StatusCounts: make(map[int]int),
		Latencies:    make([]time.Duration, 0, cfg.Total),
	}

	// Drain results in the main goroutine. We wait for `wg.Wait()` in a
	// separate goroutine and close `results` once it's done, which is
	// what terminates this range loop.
	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		out.TotalRequests++
		completed.Add(1)
		if r.err != nil {
			out.Errors++
			continue
		}
		out.StatusCounts[r.statusCode]++
		out.Latencies = append(out.Latencies, r.duration)
	}

	out.Duration = time.Since(overallStart)

	// Stop the progress bar.
	<-progressDone

	sort.Slice(out.Latencies, func(i, j int) bool {
		return out.Latencies[i] < out.Latencies[j]
	})

	return out, nil
}

// showProgress redraws a progress bar to out until done is closed or
// ctx is cancelled.
func showProgress(ctx context.Context, out io.Writer, completed *atomic.Int64, total int, done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			n := int(completed.Load())
			renderProgressBar(out, n, total)
			if n >= total {
				fmt.Fprintln(out) // newline after final bar
				return
			}
		}
	}
}

// renderProgressBar writes a fixed-width bar with current/total.
// Uses \r to redraw in-place.
func renderProgressBar(out io.Writer, current, total int) {
	const width = 30
	if total == 0 {
		return
	}
	filled := current * width / total
	if filled > width {
		filled = width
	}
	bar := ""
	for i := 0; i < width; i++ {
		if i < filled {
			bar += "#"
		} else {
			bar += " "
		}
	}
	fmt.Fprintf(out, "\r[%s] %d/%d", bar, current, total)
}

// formatReport renders the result as a human-readable summary.
func formatReport(out io.Writer, result *SiegeResult) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "results:")
	fmt.Fprintf(out, "  total:      %d\n", result.TotalRequests)
	fmt.Fprintf(out, "  duration:   %s\n", result.Duration.Round(time.Millisecond))

	rps := float64(result.TotalRequests) / result.Duration.Seconds()
	fmt.Fprintf(out, "  rps:        %.1f\n", rps)

	if result.Errors > 0 {
		fmt.Fprintf(out, "  errors:     %d\n", result.Errors)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "status codes:")
	// Sort status codes for stable output.
	codes := make([]int, 0, len(result.StatusCounts))
	for c := range result.StatusCounts {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	for _, code := range codes {
		count := result.StatusCounts[code]
		pct := 100.0 * float64(count) / float64(result.TotalRequests)
		fmt.Fprintf(out, "  %d  %-20s %5d  (%.1f%%)\n", code, http.StatusText(code), count, pct)
	}

	if len(result.Latencies) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "latency:")
		fmt.Fprintf(out, "  p50:  %s\n", percentile(result.Latencies, 0.50).Round(time.Millisecond))
		fmt.Fprintf(out, "  p95:  %s\n", percentile(result.Latencies, 0.95).Round(time.Millisecond))
		fmt.Fprintf(out, "  p99:  %s\n", percentile(result.Latencies, 0.99).Round(time.Millisecond))
		fmt.Fprintf(out, "  max:  %s\n", result.Latencies[len(result.Latencies)-1].Round(time.Millisecond))
	}
}

// percentile returns the p-th percentile of a sorted slice of durations.
// p is in [0, 1], e.g. 0.95 for p95.
//
// Uses the "nearest rank" method, which is simple and matches what most
// load-testing tools (hey, ab, vegeta) produce for these summary stats.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := int(p * float64(len(sorted)))
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// isTerminal returns true if w is a terminal (stdout to a real shell).
// Used to decide whether to render the progress bar.
//
// Uses file mode detection: TTYs are character devices. Pipes and files
// aren't. No external dep.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
