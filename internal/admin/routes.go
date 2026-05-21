package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/LightAwesome/portcullis/internal/crypto"
	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/store"
)

// CreateRouteRequest is the JSON body for POST /admin/routes.
//
// UpstreamSecret is the real API key for the upstream service (e.g. the
// real OpenAI key). It's encrypted with AES-256-GCM using the gateway's
// master key before being written to Postgres; the plaintext never
// touches disk and never leaves this handler.
type CreateRouteRequest struct {
	Prefix         string `json:"prefix"`
	TargetBaseURL  string `json:"target_base_url"`
	UpstreamSecret string `json:"upstream_secret"`
}

// CreateRouteResponse is the JSON body returned on success.
//
// Notably absent: the upstream secret. It went in but never comes out via
// admin endpoints. The only way to use it is via the proxy itself.
type CreateRouteResponse struct {
	ID            string    `json:"id"`
	Prefix        string    `json:"prefix"`
	TargetBaseURL string    `json:"target_base_url"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
}

// HandleCreateRoute returns the handler for POST /admin/routes.
//
// Validates prefix, target URL, and upstream secret presence. The
// upstream secret is encrypted with AES-256-GCM using masterKey before
// being passed to the store; only ciphertext is persisted.
//
// masterKey must be exactly 32 bytes (AES-256). The caller is responsible
// for capturing it once at startup from config.MasterKeyBytes().
func HandleCreateRoute(db *store.Store, masterKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateRouteRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}

		req.Prefix = strings.TrimSpace(req.Prefix)
		req.TargetBaseURL = strings.TrimSpace(req.TargetBaseURL)

		if req.Prefix == "" {
			httpx.WriteError(w, http.StatusBadRequest,
				"missing_prefix", "prefix is required")
			return
		}
		if len(req.Prefix) > 50 {
			httpx.WriteError(w, http.StatusBadRequest,
				"prefix_too_long", "prefix must be 50 characters or fewer")
			return
		}
		if !isValidPrefix(req.Prefix) {
			httpx.WriteError(w, http.StatusBadRequest,
				"invalid_prefix", "prefix may contain only lowercase letters, digits, and hyphens")
			return
		}

		if req.TargetBaseURL == "" {
			httpx.WriteError(w, http.StatusBadRequest,
				"missing_target_url", "target_base_url is required")
			return
		}
		if err := validateURL(req.TargetBaseURL); err != nil {
			httpx.WriteError(w, http.StatusBadRequest,
				"invalid_target_url", err.Error())
			return
		}

		if req.UpstreamSecret == "" {
			httpx.WriteError(w, http.StatusBadRequest,
				"missing_upstream_secret", "upstream_secret is required")
			return
		}

		// Encrypt before storing. crypto.Encrypt produces nonce ‖ ciphertext ‖ tag,
		// ready to be stored in upstream_routes.upstream_secret_ciphertext.
		secretCiphertext, err := crypto.Encrypt([]byte(req.UpstreamSecret), masterKey)
		if err != nil {
			// Encrypt fails only on wrong key length or crypto/rand failure.
			// Both indicate misconfiguration that validate() should have
			// caught; treat as a server bug, not a client error.
			httpx.WriteError(w, http.StatusInternalServerError,
				"encrypt_failed", "the keep's seal could not be cast")
			return
		}

		route, err := db.CreateRoute(r.Context(), req.Prefix, req.TargetBaseURL, secretCiphertext)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				httpx.WriteError(w, http.StatusConflict,
					"prefix_taken", "a keep with this prefix already exists")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError,
				"create_failed", "could not register keep")
			return
		}

		uuidBytes, _ := route.ID.Value()
		resp := CreateRouteResponse{
			ID:            formatUUIDValue(uuidBytes),
			Prefix:        route.Prefix,
			TargetBaseURL: route.TargetBaseURL,
			IsActive:      route.IsActive,
			CreatedAt:     route.CreatedAt,
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// isValidPrefix reports whether s is a valid route prefix:
// lowercase letters, digits, hyphens; non-empty.
//
// Restricted character set avoids URL-escaping issues and makes prefixes
// safe to embed in paths verbatim.
func isValidPrefix(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return len(s) > 0
}
