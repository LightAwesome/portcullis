package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

// newDirector returns the Director function for an httputil.ReverseProxy
// configured for a specific upstream.
//
// The director is called once per proxied request, with the same *Request
// the gateway received (now copied — mutating it is safe). It must:
//   - Set req.URL.Scheme, req.URL.Host to the upstream's
//   - Rewrite req.URL.Path to drop our /proxy/{prefix} prefix and join
//     the upstream's base path
//   - Set req.Host to the upstream's host (so the upstream sees the right
//     Host header)
//   - Strip our gateway-specific headers (X-Gateway-Key)
//   - Inject the upstream secret as Authorization: Bearer <secret>
func newDirector(target *url.URL, prefix string, upstreamSecret []byte) func(*http.Request) {
	return func(req *http.Request) {
		// 1. Rewrite scheme and host to point at the upstream.
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host

		// 2. Compute the new path. Strip our /proxy/{prefix} from the
		//    incoming path, then join with the upstream's base path.
		incomingPath := req.URL.Path
		strippedPath := trimGatewayPrefix(incomingPath, prefix)
		req.URL.Path, req.URL.RawPath = joinPath(target, strippedPath)

		// 3. Set the Host header to the upstream's. Without this, the
		//    upstream sees Host: <gateway>, which breaks routing on
		//    name-based virtual hosts (most production HTTP APIs).
		req.Host = target.Host

		// 4. Strip gateway-specific headers. The hop-by-hop headers are
		//    handled by ReverseProxy itself; X-Gateway-Key is ours and
		//    must be removed explicitly so it doesn't leak upstream.
		req.Header.Del("X-Gateway-Key")

		// 5. Inject the upstream secret as Bearer auth.
		//    Phase 1 default: Authorization: Bearer <secret>. Per-route
		//    header customisation is a Phase 6 feature.
		if len(upstreamSecret) > 0 {
			req.Header.Set("Authorization", "Bearer "+string(upstreamSecret))
		}

		// 6. Set User-Agent to identify ourselves. ReverseProxy preserves
		//    the client's User-Agent by default; we override to make it
		//    obvious to upstream operators that requests came via Portcullis.
		//    Helpful for debugging and abuse triage.
		req.Header.Set("User-Agent", "Portcullis/1.0 (api-gateway)")
	}
}

// trimGatewayPrefix removes "/proxy/<prefix>" from the front of path.
// Examples:
//
//	("/proxy/httpbin/get", "httpbin")  -> "/get"
//	("/proxy/httpbin", "httpbin")      -> "/"
//	("/proxy/httpbin/", "httpbin")     -> "/"
func trimGatewayPrefix(path, prefix string) string {
	stripped := strings.TrimPrefix(path, "/proxy/"+prefix)
	if stripped == "" || stripped == "/" {
		return "/"
	}
	return stripped
}

// joinPath joins the upstream's base path with the trimmed gateway path,
// returning both the decoded Path and the encoded RawPath (the latter is
// what's actually sent on the wire).
//
// Example:
//
//	target.Path = "/v1", trimmed = "/chat/completions"
//	-> ("/v1/chat/completions", "/v1/chat/completions")
//
// We do this manually rather than using url.URL.ResolveReference because
// ResolveReference's semantics (RFC 3986) treat /paths as absolute,
// discarding the target's base path — wrong for our case where we want
// to PREPEND the target's base path.
func joinPath(target *url.URL, trimmed string) (string, string) {
	base := target.Path
	if base == "" {
		base = "/"
	}

	// Combine; avoid double-slash at the join.
	var path string
	switch {
	case trimmed == "/" || trimmed == "":
		path = base
	case strings.HasSuffix(base, "/") && strings.HasPrefix(trimmed, "/"):
		path = base + trimmed[1:]
	case !strings.HasSuffix(base, "/") && !strings.HasPrefix(trimmed, "/"):
		path = base + "/" + trimmed
	default:
		path = base + trimmed
	}

	// We don't preserve RawPath rewriting in this Phase — most APIs
	// don't depend on it. If we ever need precise byte-for-byte path
	// preservation (e.g. for APIs that distinguish /a%2Fb from /a/b),
	// we'd compute RawPath separately. Returning the same value for
	// both means net/http re-encodes from Path on send, which is
	// correct for the vast majority of cases.
	return path, path
}
