package httpx

import (
	"context"
	"log/slog"
)

// loggerCtxKey is unexported so other packages can't collide with us.
type loggerCtxKey struct{}

// WithLogger returns a derived context carrying lg.
//
// Middleware attaches a per-request logger (with request_id, etc.)
// via this helper; downstream handlers retrieve via LoggerFromContext.
func WithLogger(ctx context.Context, lg *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, lg)
}

// LoggerFromContext returns the logger attached to ctx, or the default
// slog logger if none is attached.
//
// Always returns a non-nil logger — handlers can call LoggerFromContext(ctx).Info(...)
// unconditionally, with no nil checks.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if lg, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok {
		return lg
	}
	return slog.Default()
}
