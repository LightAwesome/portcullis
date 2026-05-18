package auth

import (
	"context"
	"github.com/LightAwesome/portcullis/internal/store"
)

// contextKey is an unexported type used as the key for context values
// stored by middleware in this package. Using a custom type prevents
// collisions with values stored under string keys in other packages.
type contextKey int

const (
	clientCtxKey contextKey = iota
)

// WithClient returns a derived context carrying c.
//
// Auth middleware uses this to pass the authenticated client to downstream
// handlers and middleware (rate limiter, proxy, etc.).
func WithClient(ctx context.Context, c *store.Client) context.Context {
	return context.WithValue(ctx, clientCtxKey, c)
}

// ClientFromContext returns the authenticated client attached to the
// request's context by authMiddleware. The bool is false if the request
// hasn't been authenticated (e.g., on /health, /metrics).
//
// Handlers that *require* a client should check the bool; their middleware
// chain should include authMiddleware, but defense in depth is cheap.
func ClientFromContext(ctx context.Context) (*store.Client, bool) {
	c, ok := ctx.Value(clientCtxKey).(*store.Client)
	return c, ok
}
