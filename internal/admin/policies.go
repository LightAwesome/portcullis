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

// CreateRateLimitPolicyRequest is the JSON body for POST /admin/policies.
//
// ClientID must be the UUID of an existing client. RoutePrefix is the
// route the policy applies to; it's NOT validated against existing
// upstream_routes (policies can outlive routes — see PRD §5.1).
type CreateRateLimitPolicyRequest struct {
	ClientID      string `json:"client_id"`
	RoutePrefix   string `json:"route_prefix"`
	MaxRequests   int    `json:"max_requests"`
	WindowSeconds int    `json:"window_seconds"`
}

// CreateRateLimitPolicyResponse is the JSON body returned on success.
//
// CreatedAt is set to the response time; we don't currently store a
// creation timestamp on policies (the schema deliberately omits it —
// policies are config, not records). Including it in the response for
// API consistency with /clients and /routes is purely cosmetic.
type CreateRateLimitPolicyResponse struct {
	ClientID      string    `json:"client_id"`
	RoutePrefix   string    `json:"route_prefix"`
	MaxRequests   int       `json:"max_requests"`
	WindowSeconds int       `json:"window_seconds"`
	CreatedAt     time.Time `json:"created_at"`
}

// HandleCreateRateLimitPolicy returns the handler for POST /admin/policies.
//
// Upsert semantics: posting the same (client_id, route_prefix) pair twice
// with different values updates the existing policy. Always returns 201 —
// see ticket P2.5 for the reasoning.
func HandleCreateRateLimitPolicy(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateRateLimitPolicyRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}

		req.ClientID = strings.TrimSpace(req.ClientID)
		req.RoutePrefix = strings.TrimSpace(req.RoutePrefix)

		if req.ClientID == "" {
			httpx.WriteError(w, http.StatusBadRequest,
				"missing_client_id", "client_id is required")
			return
		}
		if req.RoutePrefix == "" {
			httpx.WriteError(w, http.StatusBadRequest,
				"missing_route_prefix", "route_prefix is required")
			return
		}
		if !isValidPrefix(req.RoutePrefix) {
			httpx.WriteError(w, http.StatusBadRequest,
				"invalid_route_prefix", "route_prefix may contain only lowercase letters, digits, and hyphens")
			return
		}
		if req.MaxRequests <= 0 {
			httpx.WriteError(w, http.StatusBadRequest,
				"invalid_max_requests", "max_requests must be a positive integer")
			return
		}
		if req.WindowSeconds <= 0 {
			httpx.WriteError(w, http.StatusBadRequest,
				"invalid_window_seconds", "window_seconds must be a positive integer")
			return
		}

		policy, err := db.CreateRateLimitPolicy(
			r.Context(),
			req.ClientID,
			req.RoutePrefix,
			req.MaxRequests,
			req.WindowSeconds,
		)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Translated from Postgres foreign-key violation: client doesn't exist.
				httpx.WriteError(w, http.StatusNotFound,
					"unknown_client", "no garrison with that id")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError,
				"create_failed", "could not record the policy")
			return
		}

		resp := CreateRateLimitPolicyResponse{
			ClientID:      policy.ClientID,
			RoutePrefix:   policy.RoutePrefix,
			MaxRequests:   policy.MaxRequests,
			WindowSeconds: policy.WindowSeconds,
			CreatedAt:     time.Now().UTC(),
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
