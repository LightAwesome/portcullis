// Package ratelimit implements the gateway's rate-limit middleware.
//
// The middleware reads the authenticated client from the request context
// (set by auth middleware upstream), reads the route prefix from the URL,
// looks up the policy, and consults the atomic Lua-scripted check-and-
// increment in the store. On allow, sets X-RateLimit-* headers and calls
// next. On deny, returns 429 with Retry-After and a themed body.
//
// The actual rate-limit primitives live in the store package; this
// package owns only the HTTP-layer wiring.
package ratelimit

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"fmt"
	"github.com/LightAwesome/portcullis/internal/auth"
	"github.com/LightAwesome/portcullis/internal/httpx"
	"github.com/LightAwesome/portcullis/internal/store"
	"github.com/go-chi/chi/v5"
)

// Middleware returns a Chi middleware that rate-limits requests.
//
// Must run AFTER auth middleware — relies on server.ClientFromContext to
// identify the caller. Must run BEFORE the proxy handler so denied
// requests never reach the upstream.
//
// Behaviour on store failure (Postgres or Redis unhealthy): fail OPEN.
// The middleware logs and proceeds without rate limiting. The reasoning
// is in PRD §10: better to serve degraded traffic than take an outage
// when the limiter itself is unhealthy.
//
// func Middleware(db *store.Store) func(http.Handler) http.Handler {
// 	return func(next http.Handler) http.Handler {
// 		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
// 			fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: hit, path=%s\n", r.URL.Path)
// 			client, ok := auth.ClientFromContext(r.Context())
//
// 			if !ok {
// 				fmt.Fprintln(os.Stderr, "RL MIDDLEWARE: no client in context")
// 				httpx.WriteError(w, http.StatusInternalServerError, "rate_limit_no_client", "the gate is confused")
// 				return
// 			}
// 			fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: client found, id=%v\n", client.ID)
//
// 			prefix := chi.URLParam(r, "prefix")
// 			fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: prefix=%q\n", prefix)
//
// 			if prefix == "" {
// 				fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: prefix not found.\n")
// 				httpx.WriteError(w, http.StatusInternalServerError, "rate_limit_no_prefix", "the gate is confused")
// 				return
// 			}
//
// 			clientIDString := clientIDtoString(client)
// 			fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: clientIDString=%q\n", clientIDString)
//
// 			if clientIDString == "" {
// 				httpx.WriteError(w, http.StatusInternalServerError, "rate_limit_bad_client_id", "the gate is confused")
// 				return
//
// 			}
//
// 			policy, err := db.GetRateLimitPolicy(r.Context(), clientIDString, prefix)
// 			if err != nil {
// 				fmt.Fprintf(os.Stderr, "RL MIDDLEWARE POLICY: error=%q\n", err)
// 				next.ServeHTTP(w, r)
// 				//TODO: Log the error but allow the request and fail open
// 			}
//
// 			nowMS := time.Now().UnixMilli()
// 			result, err := db.CheckRateLimit(r.Context(), clientIDString, prefix, int64(policy.MaxRequests), int64(policy.WindowSeconds), nowMS)
//
// 			if err != nil {
// 				fmt.Fprintf(os.Stderr, "RL MIDDLEWARE RESULT: error=%q\n", err)
// 				next.ServeHTTP(w, r)
// 				// Serve the request even if rate limiting fails.
//
// 			}
//
// 			setRateLimitHeaders(w, policy.MaxRequests, result)
//
// 			if !result.Allowed {
// 				fmt.Fprintf(os.Stderr, "RL MIDDLEWARE RATE LIMITED:\n")
// 				retryAfter := retryAfter(result.ResetMS, nowMS)
// 				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
// 				fmt.Fprintf(os.Stderr, "RL MIDDLEWARE RETRY AFTER: %v\n", retryAfter)
//
// 				httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "the portcullis falls - Try again")
// 				return
// 			}
//
// 			next.ServeHTTP(w, r)
//
// 		})
//
// 	}
// }

func Middleware(db *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: hit, path=%s\n", r.URL.Path)

			client, ok := auth.ClientFromContext(r.Context())
			if !ok {
				fmt.Fprintln(os.Stderr, "RL MIDDLEWARE: no client in context")
				httpx.WriteError(w, http.StatusInternalServerError,
					"rate_limit_no_client", "the gate is confused")
				return
			}

			fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: client found, id=%v\n", client.ID)

			prefix := chi.URLParam(r, "prefix")
			fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: prefix=%q\n", prefix)

			if prefix == "" {
				fmt.Fprintln(os.Stderr, "RL MIDDLEWARE: prefix not found")
				httpx.WriteError(w, http.StatusInternalServerError,
					"rate_limit_no_prefix", "the gate is confused")
				return
			}

			clientIDString := clientIDtoString(client)
			fmt.Fprintf(os.Stderr, "RL MIDDLEWARE: clientIDString=%q\n", clientIDString)

			if clientIDString == "" {
				httpx.WriteError(w, http.StatusInternalServerError,
					"rate_limit_bad_client_id", "the gate is confused")
				return
			}

			policy, err := db.GetRateLimitPolicy(r.Context(), clientIDString, prefix)
			if err != nil {
				fmt.Fprintf(os.Stderr, "RL MIDDLEWARE POLICY: error=%v\n", err)
				next.ServeHTTP(w, r)
				return
			}

			nowMS := time.Now().UnixMilli()

			result, err := db.CheckRateLimit(
				r.Context(),
				clientIDString,
				prefix,
				int64(policy.MaxRequests),
				int64(policy.WindowSeconds),
				nowMS,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "RL MIDDLEWARE RESULT: error=%v\n", err)
				next.ServeHTTP(w, r)
				return
			}

			setRateLimitHeaders(w, policy.MaxRequests, result)

			fmt.Fprintf(os.Stderr,
				"RL AFTER SET: allowed=%v limit=%q remaining=%q reset=%q\n",
				result.Allowed,
				w.Header().Get("X-RateLimit-Limit"),
				w.Header().Get("X-RateLimit-Remaining"),
				w.Header().Get("X-RateLimit-Reset"),
			)

			if !result.Allowed {
				fmt.Fprintln(os.Stderr, "RL MIDDLEWARE RATE LIMITED")

				retryAfterSeconds := retryAfter(result.ResetMS, nowMS)
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))

				fmt.Fprintf(os.Stderr, "RL MIDDLEWARE RETRY AFTER: %v\n", retryAfterSeconds)

				httpx.WriteError(w, http.StatusTooManyRequests,
					"rate_limited", "the portcullis falls - Try again")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

const BASE10 = 10

func retryAfter(resetMS int64, nowMS int64) int {
	deltaMS := resetMS - nowMS
	if deltaMS <= 0 {
		return 1
	}
	return int(deltaMS+999) / 1000
}

// Maybe return error here? who knows if http.Header.Set is flaky or unreliable to ctach an error here
func setRateLimitHeaders(w http.ResponseWriter, limit int, result *store.RateLimitResult) {

	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, BASE10))
	resetSec := result.ResetMS / 1000
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetSec, BASE10))
}

func clientIDtoString(client *store.Client) string {
	clientID, err := client.ID.Value()

	if err != nil {
		return ""
	}

	clientIDStr, ok := clientID.(string)

	if !ok {
		return ""
	}

	return clientIDStr

}
