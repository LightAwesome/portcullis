package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// MaxRequestBodyBytes is the largest request body the server will accept.
//
// One megabyte is generous for admin JSON; anything larger is suspicious
// (or a misuse of the admin API for something it wasn't built for).
const MaxRequestBodyBytes = 1 << 20 // 1 MiB

// DecodeJSON reads and decodes a JSON request body into v.
//
// Enforces:
//   - Content-Type ignored (we don't require application/json — common bug
//     in clients, and we tolerate it because the body still parses or it
//     doesn't).
//   - Body size capped at MaxRequestBodyBytes.
//   - Unknown fields rejected — catches typos in client requests early.
//   - Single JSON value per request (no trailing data, no streaming).
//
// Returns a user-presentable error message; handlers can pass it through
// to writeError directly.
func DecodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, MaxRequestBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		// Translate common JSON errors into user-presentable messages.
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxBytesErr):
			return fmt.Errorf("request body exceeds %d bytes", MaxRequestBodyBytes)
		case errors.Is(err, io.EOF):
			return errors.New("request body is empty")
		default:
			return fmt.Errorf("invalid JSON: %s", err)
		}
	}

	// Reject trailing data: a request body of `{"x":1}{"y":2}` should not
	// be accepted as `{"x":1}`.
	if dec.More() {
		return errors.New("multiple JSON values in body")
	}

	return nil
}
