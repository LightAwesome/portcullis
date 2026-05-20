package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/LightAwesome/portcullis/internal/auth"
	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/store"
)

// authMiddleware returns a Chi middleware that authenticates requests via
// the X-Gateway-Key header.
//
// On success, the authenticated *store.Client is attached to the request
// context (retrieve via ClientFromContext) and the chain continues.
//
// On failure (missing header, malformed key, unknown keyID, wrong secret,
// inactive client), responds with 401 and a themed error body. The chain
// is short-circuited.
func authMiddleware(deps *Dependencies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := r.Header.Get("X-Gateway-Key")
			if rawKey == "" {
				httpx.WriteError(w, http.StatusUnauthorized,
					"no_key", "halt — no banner, no entry")
				return
			}

			parsed, err := auth.ParseKey(rawKey)
			if err != nil {
				httpx.WriteError(w, http.StatusUnauthorized,
					"malformed_key", "that banner is not recognised at this gate")
				return
			}

			client, err := deps.Store.GetClientByKeyID(r.Context(), parsed.KeyID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					httpx.WriteError(w, http.StatusUnauthorized,
						"unknown_key", "that banner is not recognised at this gate")
					return
				}
				// Real backend error — log path will be added in P3.
				httpx.WriteError(w, http.StatusInternalServerError,
					"auth_lookup_failed", "the gate cannot verify your banner")
				return
			}

			if !client.IsActive {
				httpx.WriteError(w, http.StatusUnauthorized,
					"inactive_key", "that banner has been struck from the rolls")
				return
			}

			ok, err := deps.Authenticator.Verify(parsed.Secret, client.KeyHash)
			if err != nil || !ok {
				httpx.WriteError(w, http.StatusUnauthorized,
					"invalid_key", "that banner is not recognised at this gate")
				return
			}

			// Attach the authenticated client to the request context for
			// downstream handlers (proxy, rate limiter, log writer).
			ctx := auth.WithClient(r.Context(), client)
			if labels := httpx.MetricLabelsFromContext(ctx); labels != nil {
				labels.Client = client.Name
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func adminAuthMiddleware(deps *Dependencies) func(http.Handler) http.Handler {
	expected := []byte(deps.Config.AdminKey)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := []byte(r.Header.Get("X-Admin-Key"))

			if subtle.ConstantTimeCompare(provided, expected) != 1 {
				httpx.WriteError(w, http.StatusUnauthorized, "admin_auth_failed", "the keep is closed to you")
				return
			}

			next.ServeHTTP(w, r)

		})
	}
}

// requestIDMiddleware attaches a request ID to each request.
//
// If the client sent X-Request-ID, that's used (truncated to a sane length).
// Otherwise we generate a random 16-char hex value. The ID is set as a
// response header so clients can reference it, and attached to the
// context for logger correlation.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" || len(reqID) > 64 {
			reqID = generateRequestID()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), requestIDCtxKey{}, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestIDCtxKey is the context key for the request ID string.
type requestIDCtxKey struct{}

// RequestIDFromContext returns the request ID attached by requestIDMiddleware,
// or "" if none is attached.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDCtxKey{}).(string); ok {
		return id
	}
	return ""
}

// generateRequestID returns a random 16-char hex string.
//
// Used when the client didn't supply X-Request-ID. crypto/rand is overkill
// for an ID (we don't need it to be unguessable, just unique), but it's
// stdlib and free.
func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand on a real OS doesn't fail. If it ever does, returning
		// a fixed-ish value is better than crashing.
		return "req-fallback"
	}
	return hex.EncodeToString(b)
}

// loggerMiddleware attaches a per-request logger to the context.
//
// The logger is derived from the base logger with the request_id attribute,
// so every log call from downstream handlers automatically includes the ID.
// Runs AFTER requestIDMiddleware (which sets the ID).
func loggerMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestIDFromContext(r.Context())
			lg := base.With("request_id", reqID)
			ctx := httpx.WithLogger(r.Context(), lg)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// accessLogMiddleware logs every request after it completes.
//
// We use a wrapped ResponseWriter to capture the status code, which
// stdlib's ResponseWriter doesn't expose. Bytes written would also
// be useful; we'll add that field in P3.4 when we wire up the chronicle.
func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap the writer to capture status code.
		ww := &statusCapturingWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(ww, r)

		duration := time.Since(start)
		lg := httpx.LoggerFromContext(r.Context())

		// Use Warn for slow requests, Info otherwise. Distinguishes
		// in dashboards without log-grep gymnastics.
		level := slog.LevelInfo
		if duration > 5*time.Second {
			level = slog.LevelWarn
		}

		lg.LogAttrs(r.Context(), level, "request completed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", ww.statusCode),
			slog.Duration("duration", duration),
		)
	})
}

// statusCapturingWriter wraps http.ResponseWriter to record the status code.
type statusCapturingWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}
