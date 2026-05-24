// Package breaker implements a per-route circuit breaker.
//
// Each upstream service (identified by route prefix in the gateway)
// gets its own Breaker. The breaker watches consecutive failures within
// a sliding time window: enough failures trip the circuit "open," after
// which the breaker short-circuits all requests until a cool-down
// elapses. A single probe is then permitted ("half-open"); success
// closes the circuit, failure re-opens it.
//
// Design notes:
//
//   - State is in-memory per process. Multiple gateway instances would
//     each have independent breaker state — acceptable for our single-
//     instance MVP. Redis-backed state would be the next evolution.
//
//   - Mutex over atomics because state transitions involve multiple
//     fields (state + counter + timestamps). The contention is low —
//     one quick critical section per request — so the simpler primitive
//     is the right call.
//
//   - "Failure" is defined by the caller (the proxy handler), not the
//     breaker. The breaker only counts what's reported. For our gateway,
//     failures are 5xx responses and connection errors; 4xx responses
//     and rate-limits are NOT failures.
//
//   - The Clock interface is purely for testability. Real code uses
//     realClock{}; tests inject a fakeClock for deterministic timing.
//
// API:
//
//	b := NewBreaker(Config{Name: "openai", ...})
//
//	if !b.Allow() {
//	    // Short-circuit with 503. Don't call upstream.
//	    return
//	}
//
//	err := doUpstreamRequest()
//	if isFailure(err) {
//	    b.ReportFailure()
//	} else {
//	    b.ReportSuccess()
//	}
//
// The Allow/Report pattern is awkward but necessary: Go's
// httputil.ReverseProxy doesn't expose a single function we can wrap.
// Request lifecycle is split across Director, ModifyResponse, and
// ErrorHandler, all of which fire at different points.
package breaker

import (
	"sync"
	"time"
)

// State is the current state of a Breaker.
//
// Values are ordered so that the integer value increases with severity:
//
//	StateClosed   = 0  (normal operation)
//	StateHalfOpen = 1  (probing recovery)
//	StateOpen     = 2  (short-circuiting)
//
// This ordering matches the natural reading of Prometheus gauges
// ("higher means worse") and makes Grafana dashboards intuitive.
type State int

const (
	StateClosed State = iota
	StateHalfOpen
	StateOpen
)

// String returns a human-readable name for the state. Used in logs,
// error messages, and Prometheus labels.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}

// Clock abstracts time for testability. Real callers use realClock{}.
// Tests inject a fake clock to drive transitions deterministically.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
}

type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }

// Config bundles construction parameters for a Breaker. Sensible
// defaults are applied for zero-valued fields.
type Config struct {
	// Name identifies this breaker in logs and metrics. Typically
	// the route prefix it protects (e.g., "openai", "anthropic").
	Name string

	// FailureThreshold is the number of consecutive failures within
	// Window that trips the breaker open. Default: 5.
	FailureThreshold int

	// Window is the rolling time window for counting failures. Failures
	// older than this don't count toward the threshold. Default: 60s.
	Window time.Duration

	// Cooldown is how long the breaker stays open before allowing a
	// half-open probe. Default: 30s.
	Cooldown time.Duration

	// Clock is the time source. Defaults to a real clock; tests
	// inject a fake one.
	Clock Clock

	// OnStateChange is called every time the breaker transitions
	// between states. Optional. Used for Prometheus metrics in
	// production; for assertions in tests. The callback runs while
	// the breaker's lock is held — keep it brief and non-blocking.
	OnStateChange func(from, to State)
}

// Breaker is a circuit breaker for a single upstream service.
//
// Safe for concurrent use. All operations acquire b.mu briefly.
type Breaker struct {
	name             string
	failureThreshold int
	window           time.Duration
	cooldown         time.Duration
	clock            Clock
	onStateChange    func(from, to State)

	// State, all protected by mu.
	mu            sync.Mutex
	state         State
	failureCount  int       // consecutive failures within window
	firstFailure  time.Time // when the current streak started
	openedAt      time.Time // when state became Open (for cooldown timing)
	probeInFlight bool      // a half-open probe is currently active
}

// NewBreaker constructs a Breaker with the given config. Zero-valued
// fields receive sensible defaults.
func NewBreaker(cfg Config) *Breaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.Window <= 0 {
		cfg.Window = 60 * time.Second
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}

	return &Breaker{
		name:             cfg.Name,
		failureThreshold: cfg.FailureThreshold,
		window:           cfg.Window,
		cooldown:         cfg.Cooldown,
		clock:            cfg.Clock,
		onStateChange:    cfg.OnStateChange,
		state:            StateClosed,
	}
}

// Allow reports whether the caller may proceed with an upstream request.
//
// Returns true in two cases:
//   - The breaker is closed (normal operation).
//   - The breaker is half-open and no probe is currently in flight.
//
// Returns false when:
//   - The breaker is open and cooldown has not elapsed.
//   - The breaker is half-open but another probe is in flight.
//
// On returning true from a half-open call, the breaker remembers that
// a probe is in flight; subsequent Allow calls return false until the
// caller invokes ReportSuccess or ReportFailure.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return true

	case StateOpen:
		// Has the cooldown elapsed? If so, transition to half-open
		// and let this caller be the probe.
		if b.clock.Since(b.openedAt) >= b.cooldown {
			b.transition(StateHalfOpen)
			b.probeInFlight = true
			return true
		}
		return false

	case StateHalfOpen:
		// Exactly one probe in flight at a time. The first caller
		// after entering half-open got probeInFlight = true; subsequent
		// callers must wait for that probe to resolve.
		if b.probeInFlight {
			return false
		}
		b.probeInFlight = true
		return true

	default:
		// Defensive: an unknown state should never happen, but if it
		// does, prefer short-circuit over allow.
		return false
	}
}

// ReportSuccess tells the breaker that the most recent request succeeded.
//
// In closed state: resets the failure counter (streak is broken).
// In half-open state: closes the circuit, clears probe flag.
// In open state: no-op (a success while open shouldn't happen — Allow
// would have returned false — but if it does, ignore it).
func (b *Breaker) ReportSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		// Reset the failure counter. A success ends the current
		// failure streak.
		b.failureCount = 0

	case StateHalfOpen:
		// The probe succeeded. Recovery confirmed. Close the circuit.
		b.probeInFlight = false
		b.failureCount = 0
		b.transition(StateClosed)

	case StateOpen:
		// Shouldn't happen — Allow returned false to this caller.
		// Tolerate gracefully.
		return
	}
}

// ReportFailure tells the breaker that the most recent request failed.
//
// In closed state: increments failure count (resetting if the previous
// streak fell outside the window). If the threshold is hit, transitions
// to open.
//
// In half-open state: the probe failed. Returns to open with a fresh
// cooldown timer. The current request was the only one allowed through
// during half-open, so probeInFlight is cleared.
//
// In open state: no-op (same reason as ReportSuccess above).
func (b *Breaker) ReportFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		// Is this failure part of the current streak, or starting a
		// new one? "Streak" = continuous failures within the window.
		now := b.clock.Now()
		if b.failureCount == 0 || b.clock.Since(b.firstFailure) >= b.window {
			// Start a fresh streak.
			b.failureCount = 1
			b.firstFailure = now
		} else {
			// Continue the current streak.
			b.failureCount++
		}

		// Trip the breaker if the threshold is hit.
		if b.failureCount >= b.failureThreshold {
			b.openedAt = now
			b.transition(StateOpen)
		}

	case StateHalfOpen:
		// Probe failed. Recovery hasn't happened. Re-open with fresh
		// cooldown. The probeInFlight flag is cleared.
		b.probeInFlight = false
		b.openedAt = b.clock.Now()
		b.transition(StateOpen)

	case StateOpen:
		// Shouldn't happen — Allow returned false to this caller.
		// Tolerate gracefully.
		return
	}
}

// State returns the current state of the breaker. Useful for
// metrics, logging, and debugging.
//
// Note: the state can change between when this function returns and
// when the caller acts on the result. Don't use it for control flow —
// use Allow() for that.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// transition changes state and invokes the callback. Must be called
// with b.mu held.
func (b *Breaker) transition(to State) {
	from := b.state
	if from == to {
		return
	}
	b.state = to
	if b.onStateChange != nil {
		b.onStateChange(from, to)
	}
}
