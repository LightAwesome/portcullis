package breaker_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LightAwesome/portcullis/internal/breaker"
)

// fakeClock is a deterministic clock for tests. Time only advances
// when the test explicitly calls Advance. Safe for concurrent use
// because the breaker tests do read time from multiple goroutines
// during the concurrency tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Since(t time.Time) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now.Sub(t)
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// newTestBreaker returns a Breaker with small thresholds for fast tests.
func newTestBreaker(clk breaker.Clock) *breaker.Breaker {
	return breaker.NewBreaker(breaker.Config{
		Name:             "test",
		FailureThreshold: 3,
		Window:           60 * time.Second,
		Cooldown:         30 * time.Second,
		Clock:            clk,
	})
}

func TestBreaker_StartsClosed(t *testing.T) {
	b := newTestBreaker(newFakeClock())
	if s := b.State(); s != breaker.StateClosed {
		t.Errorf("initial state: got %v, want closed", s)
	}
	if !b.Allow() {
		t.Error("Allow returned false on closed breaker")
	}
}

func TestBreaker_TripsOnThreshold(t *testing.T) {
	clk := newFakeClock()
	b := newTestBreaker(clk)

	// 3 consecutive failures should trip.
	for i := 0; i < 3; i++ {
		if !b.Allow() {
			t.Fatalf("Allow #%d returned false while closed", i)
		}
		b.ReportFailure()
	}

	if s := b.State(); s != breaker.StateOpen {
		t.Errorf("after 3 failures: got state %v, want open", s)
	}

	// Now Allow should short-circuit.
	if b.Allow() {
		t.Error("Allow returned true on open breaker before cooldown")
	}
}

func TestBreaker_SuccessResetsCounter(t *testing.T) {
	clk := newFakeClock()
	b := newTestBreaker(clk)

	// 2 failures (under threshold) ...
	for i := 0; i < 2; i++ {
		b.Allow()
		b.ReportFailure()
	}
	// ... then a success.
	b.Allow()
	b.ReportSuccess()

	// 2 more failures should NOT trip (counter was reset).
	for i := 0; i < 2; i++ {
		b.Allow()
		b.ReportFailure()
	}

	if s := b.State(); s != breaker.StateClosed {
		t.Errorf("after success-reset and 2 more failures: got %v, want closed", s)
	}
}

func TestBreaker_WindowResetsCounter(t *testing.T) {
	clk := newFakeClock()
	b := newTestBreaker(clk)

	// 2 failures, then advance past the window.
	for i := 0; i < 2; i++ {
		b.Allow()
		b.ReportFailure()
	}
	clk.Advance(61 * time.Second)

	// One more failure should restart the streak, not trip.
	b.Allow()
	b.ReportFailure()

	if s := b.State(); s != breaker.StateClosed {
		t.Errorf("after window expiry: got %v, want closed", s)
	}
}

func TestBreaker_CooldownEnablesHalfOpen(t *testing.T) {
	clk := newFakeClock()
	b := newTestBreaker(clk)

	// Trip the breaker.
	for i := 0; i < 3; i++ {
		b.Allow()
		b.ReportFailure()
	}
	if s := b.State(); s != breaker.StateOpen {
		t.Fatalf("setup: state is %v, want open", s)
	}

	// Before cooldown — Allow should still block.
	if b.Allow() {
		t.Error("Allow returned true before cooldown elapsed")
	}

	// Advance past cooldown.
	clk.Advance(31 * time.Second)

	// Now Allow should return true (probe), state should be half-open.
	if !b.Allow() {
		t.Fatal("Allow returned false after cooldown")
	}
	if s := b.State(); s != breaker.StateHalfOpen {
		t.Errorf("after cooldown probe: state is %v, want half-open", s)
	}
}

func TestBreaker_HalfOpenSuccessCloses(t *testing.T) {
	clk := newFakeClock()
	b := newTestBreaker(clk)

	// Trip and cooldown.
	for i := 0; i < 3; i++ {
		b.Allow()
		b.ReportFailure()
	}
	clk.Advance(31 * time.Second)
	b.Allow() // probe
	b.ReportSuccess()

	if s := b.State(); s != breaker.StateClosed {
		t.Errorf("after half-open success: state is %v, want closed", s)
	}

	// And the counter is fresh — 3 more failures from here should trip.
	for i := 0; i < 3; i++ {
		b.Allow()
		b.ReportFailure()
	}
	if s := b.State(); s != breaker.StateOpen {
		t.Errorf("post-close re-trip: state is %v, want open", s)
	}
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	clk := newFakeClock()
	b := newTestBreaker(clk)

	// Trip and cooldown.
	for i := 0; i < 3; i++ {
		b.Allow()
		b.ReportFailure()
	}
	clk.Advance(31 * time.Second)
	b.Allow() // probe
	b.ReportFailure()

	if s := b.State(); s != breaker.StateOpen {
		t.Errorf("after half-open failure: state is %v, want open", s)
	}

	// Cooldown should restart — Allow blocks until another 30s.
	if b.Allow() {
		t.Error("Allow returned true immediately after half-open failure (cooldown should have reset)")
	}

	// Half-cooldown — still blocked.
	clk.Advance(15 * time.Second)
	if b.Allow() {
		t.Error("Allow returned true at half-cooldown after re-open")
	}

	// Full cooldown again — now allowed.
	clk.Advance(16 * time.Second)
	if !b.Allow() {
		t.Error("Allow returned false after full second cooldown")
	}
}

// TestBreaker_OnlyOneProbeInHalfOpen is the headline concurrency test.
//
// We trip the breaker, advance time past cooldown, then fire many
// concurrent Allow() calls. Exactly one must return true; the rest
// must return false. This proves the "exactly one probe" guarantee
// of the half-open state, which is the difference between a real
// circuit breaker and a naive timeout-and-retry.
func TestBreaker_OnlyOneProbeInHalfOpen(t *testing.T) {
	clk := newFakeClock()
	b := newTestBreaker(clk)

	// Trip the breaker.
	for i := 0; i < 3; i++ {
		b.Allow()
		b.ReportFailure()
	}

	// Advance past cooldown.
	clk.Advance(31 * time.Second)

	// 100 concurrent Allow() calls. Exactly one should return true.
	const callers = 100
	var allowed atomic.Int64
	var wg sync.WaitGroup

	// Use a started barrier so all goroutines race to call Allow
	// at approximately the same instant.
	start := make(chan struct{})

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if b.Allow() {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != 1 {
		t.Errorf("concurrent Allow during half-open: got %d allowed, want exactly 1", got)
	}
}

func TestBreaker_StateChangeCallback(t *testing.T) {
	clk := newFakeClock()
	type change struct {
		from, to breaker.State
	}
	var changes []change
	var mu sync.Mutex

	b := breaker.NewBreaker(breaker.Config{
		Name:             "callback-test",
		FailureThreshold: 3,
		Window:           60 * time.Second,
		Cooldown:         30 * time.Second,
		Clock:            clk,
		OnStateChange: func(from, to breaker.State) {
			mu.Lock()
			changes = append(changes, change{from, to})
			mu.Unlock()
		},
	})

	// Trip: closed → open.
	for i := 0; i < 3; i++ {
		b.Allow()
		b.ReportFailure()
	}
	// Cooldown: open → half-open via probe.
	clk.Advance(31 * time.Second)
	b.Allow()
	// Probe succeeds: half-open → closed.
	b.ReportSuccess()

	mu.Lock()
	defer mu.Unlock()
	if len(changes) != 3 {
		t.Fatalf("got %d state changes, want 3: %+v", len(changes), changes)
	}
	want := []change{
		{breaker.StateClosed, breaker.StateOpen},
		{breaker.StateOpen, breaker.StateHalfOpen},
		{breaker.StateHalfOpen, breaker.StateClosed},
	}
	for i, c := range changes {
		if c != want[i] {
			t.Errorf("change %d: got %+v, want %+v", i, c, want[i])
		}
	}
}

// TestBreaker_ConcurrentReadsDoNotPanic exercises State() from many
// goroutines while other goroutines manipulate state via Allow/Report*.
// If any of these races, the race detector will flag it.
func TestBreaker_ConcurrentReadsDoNotPanic(t *testing.T) {
	clk := newFakeClock()
	b := newTestBreaker(clk)

	const goroutines = 50
	const ops = 200

	var wg sync.WaitGroup

	// Writers: trip, recover, repeat.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < ops; i++ {
			b.Allow()
			if i%2 == 0 {
				b.ReportFailure()
			} else {
				b.ReportSuccess()
			}
		}
	}()

	// Readers: spam State().
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				_ = b.State()
			}
		}()
	}

	wg.Wait()
	// If we reach here without a race detector panic, we're good.
}
