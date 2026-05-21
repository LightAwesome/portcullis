package server

import (
	"fmt"
	"net/http"

	"github.com/LightAwesome/portcullis/internal/admin"
	"github.com/LightAwesome/portcullis/internal/proxy"
	"github.com/LightAwesome/portcullis/internal/ratelimit"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func addRoutes(mux chi.Router, deps *Dependencies) {
	mux.Get("/health", handleHealth(deps))
	mux.Handle("/metrics", promhttp.Handler())

	// Capture the master key once. MasterKeyBytes allocates a fresh slice
	// per call; binding here means /proxy/{prefix}/* and /proxy/{prefix}/
	// share the same 32-byte slice, and the admin route creator sees
	// bytes from the same source.
	masterKey := deps.Config.MasterKeyBytes()

	mux.Route("/proxy/{prefix}", func(r chi.Router) {
		r.Use(authMiddleware(deps))
		r.Use(chronicleMiddleware(deps.LogWorker))
		r.Use(ratelimit.Middleware(deps.Store))

		proxyHandler := proxy.Handler(deps.Store, masterKey)
		r.HandleFunc("/*", proxyHandler)
		// Bare /proxy/{prefix} (no trailing path) — the wildcard matches empty.
		r.HandleFunc("/", proxyHandler)
	})

	mux.Route("/admin", func(r chi.Router) {
		r.Use(adminAuthMiddleware(deps))
		r.Get("/ping", handleAdminPing(deps))
		r.Post("/clients", admin.HandleCreateClient(deps.Authenticator, deps.Store))
		r.Post("/routes", admin.HandleCreateRoute(deps.Store, masterKey))
		r.Post("/policies", admin.HandleCreateRateLimitPolicy(deps.Store))
	})
}

// TODO: Could abstract this later to its own file but seems unnecessary right now.
func handleAdminPing(deps *Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"admin":"acknowledged"}`)
	}
}
