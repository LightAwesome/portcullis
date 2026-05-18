package server

import (
	"fmt"
	"net/http"

	"github.com/LightAwesome/portcullis/internal/admin"
	"github.com/LightAwesome/portcullis/internal/proxy"
	"github.com/LightAwesome/portcullis/internal/ratelimit"
	"github.com/go-chi/chi/v5"
)

func addRoutes(mux chi.Router, deps *Dependencies) {
	mux.Get("/health", handleHealth(deps))

	// Authenticated routes. The proxy handler in P1.17 will replace this
	// placeholder; we mount the middleware here now so we can manually
	// verify the auth flow before there's a real proxy to test against.
	mux.Route("/proxy/{prefix}", func(r chi.Router) {
		r.Use(authMiddleware(deps))
		r.Use(ratelimit.Middleware(deps.Store))
		// r.HandleFunc("/*", handleProxyPlaceholder(deps))
		r.HandleFunc("/*", proxy.Handler(deps.Store))
		// Bare /proxy/{prefix} (no trailing path) also works as the wildcard
		// matches empty:
		r.HandleFunc("/", proxy.Handler(deps.Store))
	})

	mux.Route("/admin", func(r chi.Router) {
		r.Use(adminAuthMiddleware(deps))
		r.Get("/ping", handleAdminPing(deps))
		r.Post("/clients", admin.HandleCreateClient(deps.Authenticator, deps.Store))
		r.Post("/routes", admin.HandleCreateRoute(deps.Store))
	})
}

// handleProxyPlaceholder returns a stub that confirms auth succeeded.
// Removed in P1.17 when the real proxy lands.
//
//	func handleProxyPlaceholder(deps *Dependencies) http.HandlerFunc {
//		return func(w http.ResponseWriter, r *http.Request) {
//			client, ok := ClientFromContext(r.Context())
//			if !ok {
//				// Defense in depth — middleware should have set this.
//				httpx.WriteError(w, http.StatusInternalServerError,
//					"no_client_in_context", "the gate is confused")
//				return
//			}
//			w.Header().Set("Content-Type", "application/json; charset=utf-8")
//			w.WriteHeader(http.StatusOK)
//			fmt.Fprintf(w, `{"authenticated_as":%q,"path":%q}`+"\n", client.Name, r.URL.Path)
//		}
//	}
//
// TODO: Could abstract this later to its own file but seems unnecessary right now.
func handleAdminPing(deps *Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"admin":"acknowledged"}`)
	}
}
