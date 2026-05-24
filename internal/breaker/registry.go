package breaker

import (
	"sync"
)

// Registry manages a collection of per-route breakers.
//
// The gateway proxies to many upstream services, each identified by a
// route prefix. Each prefix gets its own independent breaker — OpenAI
// being down shouldn't open the breaker for Anthropic. Breakers are
// created lazily: the first call to Get for a prefix creates one;
// subsequent calls return the same instance.
//
// Safe for concurrent use. Lookups go through sync.Map which is
// optimized for the read-heavy access pattern: in steady state, every
// request hits Get to look up a long-lived breaker; creation only
// happens once per new route.
type Registry struct {
	breakers sync.Map // prefix string → *Breaker
	configFn func(name string) Config
}

// NewRegistry constructs a Registry. configFn returns the Config for
// a new breaker given its route name; called lazily on first Get for
// each name. Use this hook to wire in per-route configuration in
// future (today, configFn typically ignores its argument and returns
// the same defaults for every route).
func NewRegistry(configFn func(name string) Config) *Registry {
	return &Registry{configFn: configFn}
}

// Get returns the breaker for the given route prefix, creating one
// lazily if it doesn't exist. Safe for concurrent use.
//
// The creation path uses LoadOrStore to handle the race where two
// goroutines both try to create the same breaker simultaneously —
// only one of their constructions "wins"; the other's is discarded.
func (r *Registry) Get(name string) *Breaker {
	if v, ok := r.breakers.Load(name); ok {
		return v.(*Breaker)
	}
	// First request for this route: construct a breaker.
	// LoadOrStore handles the race where two goroutines reach this
	// path simultaneously — one's *Breaker becomes garbage.
	b := NewBreaker(r.configFn(name))
	actual, _ := r.breakers.LoadOrStore(name, b)
	return actual.(*Breaker)
}

// ForEach iterates over all registered breakers. Useful for metrics
// dumps or admin endpoints. The callback must not call Get/ForEach
// recursively or it will deadlock.
func (r *Registry) ForEach(fn func(name string, b *Breaker)) {
	r.breakers.Range(func(k, v any) bool {
		fn(k.(string), v.(*Breaker))
		return true
	})
}
