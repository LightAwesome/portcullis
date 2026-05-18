package server

import (
	"crypto/subtle"
	"errors"
	"net/http"

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
