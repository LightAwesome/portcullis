// Package admin holds HTTP handlers for the gateway's admin API.
//
// Handlers are constructed by HandleX functions that take their dependencies
// directly (rather than a Dependencies struct from httpx. — this keeps each
// handler honest about what it actually needs and avoids the import cycle
// that would arise if admin imported httpx.
package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LightAwesome/portcullis/internal/auth"
	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/store"
)

// CreateClientRequest is the JSON body for POST /admin/clients.
type CreateClientRequest struct {
	Name string `json:"name"`
}

// CreateClientResponse is the JSON body returned on success.
//
// Key is the freshly generated gateway key, shown once and never recoverable.
// The Warning field communicates this contract to operators reading the
// response by hand or eye.
type CreateClientResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	KeyID     string    `json:"key_id"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	Warning   string    `json:"warning"`
}

// HandleCreateClient returns the handler for POST /admin/clients.
//
// Validates the name field, generates a fresh gateway key, stores the
// HMAC of its secret in Postgres, and returns the plaintext key in the
// response (one and only time it appears).
func HandleCreateClient(authn *auth.Authenticator, db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateClientRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}

		// Trim and validate name.
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			httpx.WriteError(w, http.StatusBadRequest,
				"missing_name", "name is required")
			return
		}
		if len(req.Name) > 100 {
			httpx.WriteError(w, http.StatusBadRequest,
				"name_too_long", "name must be 100 characters or fewer")
			return
		}

		// Generate the key. This is the only place the plaintext secret
		// exists in the process — nothing logs it, nothing returns it
		// except this handler's response.
		key, hash, err := authn.GenerateKey()
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError,
				"keygen_failed", "could not forge a banner")
			return
		}

		// Persist. ErrConflict is theoretically possible if two requests
		// generate colliding keyIDs — astronomically unlikely with 8 bytes
		// of random, but we handle it to prove diligence.
		client, err := db.CreateClient(r.Context(), req.Name, key.KeyID, hash)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				httpx.WriteError(w, http.StatusConflict,
					"key_collision", "could not register garrison; try again")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError,
				"create_failed", "could not register garrison")
			return
		}

		uuidBytes, _ := client.ID.Value()
		resp := CreateClientResponse{
			ID:        formatUUIDValue(uuidBytes),
			Name:      client.Name,
			KeyID:     client.KeyID,
			Key:       key.Raw,
			CreatedAt: client.CreatedAt,
			Warning:   "save this banner now — it will not be shown again",
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// validateURL parses a URL and applies basic sanity checks.
// Used by the route handler in the same package.
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("URL must be http or https")
	}
	if u.Host == "" {
		return errors.New("URL must have a host")
	}
	return nil
}

// formatUUIDValue renders a pgtype.UUID's underlying value as a string.
// The pgtype.UUID.Value() method returns driver.Value (an interface), which
// is awkward to stringify inline at every callsite — so we centralise.
func formatUUIDValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
