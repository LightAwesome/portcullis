package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/LightAwesome/portcullis/internal/auth"
	"github.com/LightAwesome/portcullis/internal/store"
)

// contextKey is an unexported type used as the key for context values
// stored by middleware in this package. Using a custom type prevents
// collisions with values stored under string keys in other packages.
type contextKey int

const (
	clientCtxKey contextKey = iota
)

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
				writeError(w, http.StatusUnauthorized,
					"no_key", "halt — no banner, no entry")
				return
			}

			parsed, err := auth.ParseKey(rawKey)
			if err != nil {
				writeError(w, http.StatusUnauthorized,
					"malformed_key", "that banner is not recognised at this gate")
				return
			}

			client, err := deps.Store.GetClientByKeyID(r.Context(), parsed.KeyID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeError(w, http.StatusUnauthorized,
						"unknown_key", "that banner is not recognised at this gate")
					return
				}
				// Real backend error — log path will be added in P3.
				writeError(w, http.StatusInternalServerError,
					"auth_lookup_failed", "the gate cannot verify your banner")
				return
			}

			if !client.IsActive {
				writeError(w, http.StatusUnauthorized,
					"inactive_key", "that banner has been struck from the rolls")
				return
			}

			ok, err := deps.Authenticator.Verify(parsed.Secret, client.KeyHash)
			if err != nil || !ok {
				writeError(w, http.StatusUnauthorized,
					"invalid_key", "that banner is not recognised at this gate")
				return
			}

			// Attach the authenticated client to the request context for
			// downstream handlers (proxy, rate limiter, log writer).
			ctx := context.WithValue(r.Context(), clientCtxKey, client)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
