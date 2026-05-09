package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/store"
)

// CreateRouteRequest is the JSON body for POST /admin/routes.
//
// UpstreamSecret is the real API key for the upstream service (e.g. the
// real OpenAI key). It's stored as plaintext bytes for Phase 1; AES-256-GCM
// encryption arrives in P4.2-3.
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
// Validates prefix, target URL, and upstream secret presence; persists the
// route with the secret stored as plaintext bytes. Phase 4 will encrypt
// at this layer.
func HandleCreateRoute(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateRouteRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}

		req.Prefix = strings.TrimSpace(req.Prefix)
		req.TargetBaseURL = strings.TrimSpace(req.TargetBaseURL)

		// Prefix: required, simple character set, reasonable length.
		// Restricting to lowercase + digits + hyphen avoids URL-escaping
		// surprises later.
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

		// Target URL: required, must parse, must be http/https.
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

		// Upstream secret: required, but no further validation. We don't
		// know what shape the upstream wants — could be a Bearer token,
		// could be a hex string, could be raw. We just store and forward.
		if req.UpstreamSecret == "" {
			httpx.WriteError(w, http.StatusBadRequest,
				"missing_upstream_secret", "upstream_secret is required")
			return
		}

		// TODO: encrypt the secret with AES-256-GCM here.
		secretCiphertext := []byte(req.UpstreamSecret)

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
