// Package server builds the HTTP layer for Portcullis.
//
// The package follows the dependency-injection-via-struct pattern: callers
// construct a Dependencies value, pass it to NewServer, and receive an
// http.Handler ready to be served.
//
// The handler is organized as:
//
//   - Top-level middleware (recover, request ID, logger) wraps everything.
//   - Subroutes have their own middleware chains (auth + rate limit on /proxy,
//     admin auth on /admin) configured in addRoutes.
//   - Each handler is a function returning http.HandlerFunc; dependencies
//     are captured via closure at construction time.
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LightAwesome/portcullis/internal/config"
	"github.com/LightAwesome/portcullis/internal/store"
)

// Dependencies bundles everything handlers need from the rest of the program.
//
// One instance is constructed in runServer (cli/raise.go) and passed to
// NewServer. Tests construct their own Dependencies with fakes or
// testcontainer-backed real implementations.
type Dependencies struct {
	Config *config.Config
	Store  *store.Store
	// Logger, Metrics, etc. arrive in later phases.
}

// NewServer constructs the HTTP handler for the gateway.
//
// The returned handler is a Chi router with all routes and middleware
// wired in. Caller is responsible for serving it via http.Server.
func NewServer(deps *Dependencies) http.Handler {
	mux := chi.NewRouter()
	addRoutes(mux, deps)
	return mux
}
