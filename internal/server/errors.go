package server

import (
	"encoding/json"
	"net/http"
)

// errorResponse is the JSON body for any non-success response.
//
// Error is the human-readable themed message ("halt — no banner, no entry").
// Code is the machine-readable identifier monitoring and integrations
// switch on (e.g. "no_key", "invalid_key", "rate_limited"). The pair lets
// us rewrite themed prose without breaking integrations.
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// writeError renders a themed JSON error response.
//
// Themed strings should match . Codes follow snake_case and are
// stable across themed-prose changes.
func writeError(w http.ResponseWriter, status int, code, themed string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error: themed,
		Code:  code,
	})
}
