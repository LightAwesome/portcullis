package server

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func addRoutes(mux chi.Router, deps *Dependencies) {
	mux.Get("/health", handleHealth(deps))

	// Authenticated routes. The proxy handler in P1.17 will replace this
	// placeholder; we mount the middleware here now so we can manually
	// verify the auth flow before there's a real proxy to test against.
	mux.Route("/proxy", func(r chi.Router) {
		r.Use(authMiddleware(deps))
		r.HandleFunc("/*", handleProxyPlaceholder(deps))
	})

	mux.Route("/admin", func(r chi.Router) {
		r.Use(adminAuthMiddleware(deps))
		r.Get("/ping", handleAdminPing(deps))
	})
}

// handleProxyPlaceholder returns a stub that confirms auth succeeded.
// Removed in P1.17 when the real proxy lands.
func handleProxyPlaceholder(deps *Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client, ok := ClientFromContext(r.Context())
		if !ok {
			// Defense in depth — middleware should have set this.
			writeError(w, http.StatusInternalServerError,
				"no_client_in_context", "the gate is confused")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"authenticated_as":%q,"path":%q}`+"\n", client.Name, r.URL.Path)
	}
}

func handleAdminPing(deps *Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"admin":"acknowledged"}`)
	}
}
