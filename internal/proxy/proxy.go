// Package proxy implements the gateway's reverse-proxy handler.
//
// The handler is constructed via Handler, which returns an http.HandlerFunc
// that reads the route prefix from the URL, looks up the upstream via the
// store, configures an httputil.ReverseProxy on the fly, and forwards.
//
// The auth middleware runs before this handler, so by the time we see a
// request the client is authenticated and ClientFromContext returns a
// valid *store.Client.
package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/store"
)

// upstreamTransport is the http.Transport used for all upstream requests.
//
// One transport is shared across all proxied requests (and across all
// upstream destinations) — Go's transport multiplexes connections by host
// internally, so a shared transport is correct and gives us one connection
// pool per upstream rather than per-request connection churn.
//
// Timeouts:
//   - DialContext.Timeout: 5s — give up if we can't connect in 5 seconds.
//   - ResponseHeaderTimeout: 30s — generous; some upstreams take a moment
//     to start streaming. Once headers arrive, the body can stream forever.
//   - IdleConnTimeout: 90s — close idle keep-alive connections after this.
//   - MaxIdleConnsPerHost: 10 — reasonable pool size per upstream.
//
// We do NOT set an overall request timeout — the gateway's job is to be
// transparent about long requests (e.g. LLM streaming). Per-route caps
// could be added later if needed.
var upstreamTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,

	DialContext: (&net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,

	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   5 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
}

// func Handlerpractice(db *store.Store) http.HandlerFunc {
//
//		return func(w http.ResponseWriter, r *http.Request) {
//			prefix := chi.URLParam(r, "prefix")
//			if prefix == "" {
//				//TODO: Error out
//			}
//
//			route, err := db.GetRouteByPrefix(r.Context(), prefix)
//
//		}
//	}
//
// Handler returns the reverse-proxy handler.
//
// The handler reads the route prefix from the URL parameter (set by Chi's
// route pattern /proxy/{prefix}/*), looks up the upstream config, and
// forwards. Errors at lookup time produce themed JSON; errors during
// forwarding produce themed JSON via the configured ErrorHandler.
func Handler(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := chi.URLParam(r, "prefix")
		if prefix == "" {
			httpx.WriteError(w, http.StatusBadRequest,
				"missing_prefix", "no keep specified in the path")
			return
		}

		route, err := db.GetRouteByPrefix(r.Context(), prefix)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				httpx.WriteError(w, http.StatusNotFound,
					"no_such_route", "no such keep beyond this gate")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError,
				"route_lookup_failed", "the gate cannot find the keep")
			return
		}

		if !route.IsActive {
			httpx.WriteError(w, http.StatusServiceUnavailable,
				"route_inactive", "the keep beyond this gate is closed")
			return
		}

		targetURL, err := url.Parse(route.TargetBaseURL)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError,
				"invalid_target_url", "the keep address is malformed")
			return
		}

		// Construct a ReverseProxy per request. Constructing once and
		// caching would save microseconds but introduces complications:
		// route changes would require cache invalidation, and the
		// per-request Director closure is simpler than parameterising one.
		// Worth measuring before optimising.
		rp := &httputil.ReverseProxy{
			Transport: upstreamTransport,
			Director:  newDirector(targetURL, prefix, route.UpstreamSecretCiphertext),
			ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
				handleUpstreamError(rw, err)
			},
		}

		rp.ServeHTTP(w, r)
	}
}

// handleUpstreamError translates errors from the upstream call into
// themed responses. Distinguishes timeouts from other failures.
func handleUpstreamError(w http.ResponseWriter, err error) {
	// context.DeadlineExceeded is what we'd see if the upstream hit our
	// ResponseHeaderTimeout. context.Canceled means the client disconnected.
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		httpx.WriteError(w, http.StatusGatewayTimeout,
			"upstream_timeout", "the keep beyond the gate is silent")
	case errors.Is(err, context.Canceled):
		// Client closed connection. Nothing useful to write — they're gone.
		// We could log this, but for now it's silent.
		return
	default:
		// Most common: connection refused, DNS failure, TLS error.
		// We don't bother distinguishing further at Phase 1; the message
		// is generic and the operator can check logs for specifics
		// (once P3 wires structured logging in).
		httpx.WriteError(w, http.StatusBadGateway,
			"upstream_unreachable", "the keep beyond the gate has not answered")
	}
}

// trimPrefix removes a leading "/" + s + ("/" or "") from path.
// e.g. trimPrefix("/proxy/httpbin/get", "httpbin", "/proxy") = "/get"
//
// Used by the director to compute the upstream-side path from the gateway-side path.
func trimPrefix(path, prefix, parent string) string {
	full := parent + "/" + prefix
	trimmed := strings.TrimPrefix(path, full)
	if trimmed == "" {
		return "/"
	}
	return trimmed
}
