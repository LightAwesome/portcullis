package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LightAwesome/portcullis/internal/auth"
	"github.com/LightAwesome/portcullis/internal/config"
	"github.com/LightAwesome/portcullis/internal/server"
	"github.com/LightAwesome/portcullis/internal/testutil"
)

const (
	// testPepper is the HMAC pepper used across server tests. Not a real
	// secret; the bytes are fixed so test failures are reproducible.
	testPepper = "cd4f9d1494e3705d8f3b2a071684891d8495f531671620bbee5a8c6735bed38e"

	// testAdminKey is the admin token tests use to authenticate to /admin.
	testAdminKey = "test-admin-key-must-be-long-enough-to-pass-validation"
)

// Package-level state. TestMain initializes; tests reuse.
var (
	infra *testutil.Infra
	deps  *server.Dependencies
)

// TestMain starts shared infrastructure once for the entire package.
//
// Each test that mutates state should call infra.Reset(t) at the start
// to clear tables and Redis. Tests run sequentially within the package
// (no t.Parallel()), so there's no cross-test interference beyond
// ordering — and Reset addresses that.
func TestMain(m *testing.M) {
	ctx := context.Background()

	var err error
	infra, err = testutil.StartInfra(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test infrastructure setup: %v\n", err)
		os.Exit(1)
	}

	authn, err := auth.NewAuthenticator(testPepper)
	if err != nil {
		fmt.Fprintf(os.Stderr, "authenticator: %v\n", err)
		_ = teardown()
		os.Exit(1)
	}

	deps = &server.Dependencies{
		Config: &config.Config{
			Env:                  config.EnvDevelopment,
			AdminKey:             testAdminKey,
			DefaultMaxRequests:   60,
			DefaultWindowSeconds: 60,
		},
		Store:         infra.Store,
		Authenticator: authn,
	}

	code := m.Run()
	if err := teardown(); err != nil {
		fmt.Fprintf(os.Stderr, "test teardown: %v\n", err)
	}
	os.Exit(code)
}

func teardown() error {
	infra.Stop(context.Background())
	return nil
}

// newTestHandler returns a fresh server handler against the shared deps.
// Use this in every test that needs to make HTTP calls.
func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	infra.Reset(t)
	return server.NewServer(deps)
}

// newTestHandlerWithDeps is like newTestHandler but also returns deps for
// tests that need to inspect or mutate them (e.g., tests that need the
// admin key, or want to register clients via the store directly).
func newTestHandlerWithDeps(t *testing.T) (http.Handler, *server.Dependencies) {
	t.Helper()
	infra.Reset(t)
	return server.NewServer(deps), deps
}

// === Health ===

func TestHealth_OK(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: got %q", ct)
	}

	var body struct {
		Status   string `json:"status"`
		Postgres string `json:"postgres"`
		Redis    string `json:"redis"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if body.Status != "the gate stands" {
		t.Errorf("status: got %q, want %q", body.Status, "the gate stands")
	}
	if body.Postgres != "ok" {
		t.Errorf("postgres: got %q", body.Postgres)
	}
	if body.Redis != "ok" {
		t.Errorf("redis: got %q", body.Redis)
	}
}

func TestHealth_ServeMethodIsGet(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to /health: got %d, want 405", rec.Code)
	}
}

// === Admin auth (point at a real admin route, not the dead /admin/ping) ===

func TestAdminAuth_NoKey(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/clients",
		strings.NewReader(`{"name":"x"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"admin_auth_failed"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

func TestAdminAuth_WrongKey(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/clients",
		strings.NewReader(`{"name":"x"}`))
	req.Header.Set("X-Admin-Key", "definitely-not-the-right-key-and-long-enough-to-pass-validation")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"admin_auth_failed"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

func TestAdminAuth_ValidKey(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/clients",
		strings.NewReader(`{"name":"auth-test"}`))
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
}

// === Client creation ===

func TestCreateClient_Success(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	body := strings.NewReader(`{"name":"test-garrison"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/clients", body)
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Name    string `json:"name"`
		KeyID   string `json:"key_id"`
		Key     string `json:"key"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "test-garrison" {
		t.Errorf("name: got %q", resp.Name)
	}
	if !strings.HasPrefix(resp.Key, "pck_") {
		t.Errorf("key has no prefix: %q", resp.Key)
	}
	if !strings.Contains(resp.Key, resp.KeyID) {
		t.Errorf("key %q should contain key_id %q", resp.Key, resp.KeyID)
	}
	if resp.Warning == "" {
		t.Error("warning field is empty")
	}
}

func TestCreateClient_MissingName(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/clients", body)
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"missing_name"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

func TestCreateClient_UnknownField(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	body := strings.NewReader(`{"namee":"typo"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/clients", body)
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"bad_request"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

// === Route creation ===

func TestCreateRoute_Success(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	body := strings.NewReader(`{"prefix":"test","target_base_url":"https://example.com","upstream_secret":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/routes", body)
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, hasSecret := resp["upstream_secret"]; hasSecret {
		t.Error("response should not include upstream_secret")
	}
}

func TestCreateRoute_InvalidPrefix(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	body := strings.NewReader(`{"prefix":"BAD","target_base_url":"https://example.com","upstream_secret":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/routes", body)
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"invalid_prefix"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

func TestCreateRoute_DuplicatePrefix(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	body1 := `{"prefix":"dupe","target_base_url":"https://example.com","upstream_secret":"x"}`

	req := httptest.NewRequest(http.MethodPost, "/admin/routes", strings.NewReader(body1))
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/routes", strings.NewReader(body1))
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"prefix_taken"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

// === Proxy ===

// registerSeed creates a client and a route pointing at upstream.URL.
// Returns the gateway key for the client. Tests use this to set up the
// minimum world to exercise the proxy.
func registerSeed(t *testing.T, deps *server.Dependencies, prefix, upstreamURL string) string {
	t.Helper()
	ctx := context.Background()

	key, hash, err := deps.Authenticator.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	if _, err := deps.Store.CreateClient(ctx, "proxy-test", key.KeyID, hash); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if _, err := deps.Store.CreateRoute(ctx, prefix, upstreamURL, []byte("test-secret")); err != nil {
		t.Fatalf("create route: %v", err)
	}
	return key.Raw
}

func TestProxy_ForwardsAndRewrites(t *testing.T) {
	// Stand up a fake upstream that records what it received.
	var (
		gotPath       string
		gotMethod     string
		gotAuth       string
		gotGatewayKey string
		gotHost       string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotGatewayKey = r.Header.Get("X-Gateway-Key")
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream_says":"hello"}`))
	}))
	defer upstream.Close()

	handler, deps := newTestHandlerWithDeps(t)
	gatewayKey := registerSeed(t, deps, "fake", upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/proxy/fake/some/path", nil)
	req.Header.Set("X-Gateway-Key", gatewayKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), `"upstream_says":"hello"`) {
		t.Errorf("body: got %q, want upstream's response", body)
	}

	// Verify the upstream saw a properly rewritten request.
	if gotMethod != "GET" {
		t.Errorf("method: got %q, want GET", gotMethod)
	}
	if gotPath != "/some/path" {
		t.Errorf("path: got %q, want '/some/path'", gotPath)
	}
	if gotAuth != "Bearer test-secret" {
		t.Errorf("auth: got %q, want 'Bearer test-secret'", gotAuth)
	}
	if gotGatewayKey != "" {
		t.Errorf("X-Gateway-Key should be stripped, got %q", gotGatewayKey)
	}
	// The host should be the upstream's host (httptest.Server uses 127.0.0.1:N).
	if !strings.Contains(gotHost, "127.0.0.1:") {
		t.Errorf("Host: got %q, expected upstream's host", gotHost)
	}
}

func TestProxy_NoSuchRoute(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	// Create a client but no route.
	key, hash, _ := deps.Authenticator.GenerateKey()
	if _, err := deps.Store.CreateClient(context.Background(), "x", key.KeyID, hash); err != nil {
		t.Fatalf("create client: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/proxy/nonexistent/anything", nil)
	req.Header.Set("X-Gateway-Key", key.Raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"no_such_route"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

func TestProxy_UpstreamUnreachable(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)
	// Use a target URL that refuses connections — port 1 is reserved.
	gatewayKey := registerSeed(t, deps, "broken", "http://127.0.0.1:1")

	req := httptest.NewRequest(http.MethodGet, "/proxy/broken/anything", nil)
	req.Header.Set("X-Gateway-Key", gatewayKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status: got %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"upstream_unreachable"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

func TestProxy_ForwardsBodyAndQuery(t *testing.T) {
	var (
		gotBody  []byte
		gotQuery string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, deps := newTestHandlerWithDeps(t)
	gatewayKey := registerSeed(t, deps, "fake", upstream.URL)

	req := httptest.NewRequest(http.MethodPost, "/proxy/fake/path?foo=bar",
		strings.NewReader(`{"data":"hello"}`))
	req.Header.Set("X-Gateway-Key", gatewayKey)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d", rec.Code)
	}
	if string(gotBody) != `{"data":"hello"}` {
		t.Errorf("body: got %q, want '{\"data\":\"hello\"}'", gotBody)
	}
	if gotQuery != "foo=bar" {
		t.Errorf("query: got %q, want 'foo=bar'", gotQuery)
	}
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	handler, deps := newTestHandlerWithDeps(t)
	gatewayKey := registerSeed(t, deps, "rl-test", upstream.URL)

	// Default policy is 60/60. A single request should succeed.
	req := httptest.NewRequest(http.MethodGet, "/proxy/rl-test/anything", nil)
	req.Header.Set("X-Gateway-Key", gatewayKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	if got := rec.Header().Get("X-Ratelimit-Limit"); got != "60" {
		t.Errorf("X-RateLimit-Limit: got %q, want '60'", got)
	}
	if got := rec.Header().Get("X-Ratelimit-Remaining"); got != "59" {
		t.Errorf("X-RateLimit-Remaining: got %q, want '59'", got)
	}
	if rec.Header().Get("X-Ratelimit-Reset") == "" {
		t.Error("X-RateLimit-Reset missing")
	}
}

func TestRateLimit_DeniesOverLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, deps := newTestHandlerWithDeps(t)
	gatewayKey := registerSeed(t, deps, "rl-test", upstream.URL)

	// Set a tight policy on this client/route so we can hit the limit fast.
	// First we need the client's ID — fetch it back.
	ctx := context.Background()
	parsed, _ := auth.ParseKey(gatewayKey)
	client, err := deps.Store.GetClientByKeyID(ctx, parsed.KeyID)
	if err != nil {
		t.Fatalf("lookup client: %v", err)
	}
	clientIDValue, _ := client.ID.Value()
	clientIDStr := clientIDValue.(string)

	if _, err := deps.Store.CreateRateLimitPolicy(ctx, clientIDStr, "rl-test", 2, 60); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	// Two requests should succeed, the third should be denied.
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/proxy/rl-test/x", nil)
		req.Header.Set("X-Gateway-Key", gatewayKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200; body: %s", i, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/proxy/rl-test/x", nil)
	req.Header.Set("X-Gateway-Key", gatewayKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request: got %d, want 429; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"rate_limited"`) {
		t.Errorf("body: got %q, want code 'rate_limited'", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After missing on 429")
	}
	if got := rec.Header().Get("X-Ratelimit-Remaining"); got != "0" {
		t.Errorf("X-RateLimit-Remaining on 429: got %q, want '0'", got)
	}
}

func TestRateLimit_HeadersPresentOnSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, deps := newTestHandlerWithDeps(t)
	gatewayKey := registerSeed(t, deps, "rl-test", upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/proxy/rl-test/x", nil)
	req.Header.Set("X-Gateway-Key", gatewayKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}

	// All three rate-limit headers must be present on every response.
	wantHeaders := []string{"X-Ratelimit-Limit", "X-Ratelimit-Remaining", "X-Ratelimit-Reset"}
	for _, h := range wantHeaders {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
}

// === Policies ===

// registerClientAndGetID is a helper for policy tests: registers a client,
// returns the client's UUID string (which the policy endpoint requires).
func registerClientAndGetID(t *testing.T, deps *server.Dependencies, name string) (gatewayKey, clientID string) {
	t.Helper()
	ctx := context.Background()

	key, hash, err := deps.Authenticator.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	client, err := deps.Store.CreateClient(ctx, name, key.KeyID, hash)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	uuidValue, _ := client.ID.Value()
	return key.Raw, uuidValue.(string)
}

func TestCreatePolicy_Success(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)
	_, clientID := registerClientAndGetID(t, deps, "policy-test")

	body := strings.NewReader(fmt.Sprintf(
		`{"client_id":%q,"route_prefix":"openai","max_requests":100,"window_seconds":60}`,
		clientID,
	))
	req := httptest.NewRequest(http.MethodPost, "/admin/policies", body)
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ClientID      string `json:"client_id"`
		RoutePrefix   string `json:"route_prefix"`
		MaxRequests   int    `json:"max_requests"`
		WindowSeconds int    `json:"window_seconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ClientID != clientID {
		t.Errorf("client_id: got %q, want %q", resp.ClientID, clientID)
	}
	if resp.MaxRequests != 100 || resp.WindowSeconds != 60 {
		t.Errorf("values: got %d/%d, want 100/60", resp.MaxRequests, resp.WindowSeconds)
	}
}

func TestCreatePolicy_UpsertUpdatesExisting(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)
	_, clientID := registerClientAndGetID(t, deps, "upsert-test")

	bodyTemplate := func(max int) string {
		return fmt.Sprintf(
			`{"client_id":%q,"route_prefix":"openai","max_requests":%d,"window_seconds":60}`,
			clientID, max,
		)
	}

	// First create with max=10.
	req := httptest.NewRequest(http.MethodPost, "/admin/policies",
		strings.NewReader(bodyTemplate(10)))
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d", rec.Code)
	}

	// Second "create" with max=200 — should upsert.
	req = httptest.NewRequest(http.MethodPost, "/admin/policies",
		strings.NewReader(bodyTemplate(200)))
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upsert: got %d", rec.Code)
	}

	// Verify by reading back from store.
	policy, err := deps.Store.GetRateLimitPolicy(context.Background(), clientID, "openai")
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if policy.MaxRequests != 200 {
		t.Errorf("after upsert: got %d, want 200", policy.MaxRequests)
	}
}

func TestCreatePolicy_UnknownClient(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	body := strings.NewReader(
		`{"client_id":"00000000-0000-0000-0000-000000000000","route_prefix":"x","max_requests":10,"window_seconds":60}`,
	)
	req := httptest.NewRequest(http.MethodPost, "/admin/policies", body)
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"unknown_client"`) {
		t.Errorf("body: got %q", rec.Body.String())
	}
}

func TestCreatePolicy_MissingFields(t *testing.T) {
	handler, deps := newTestHandlerWithDeps(t)

	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"no client_id", `{"route_prefix":"x","max_requests":10,"window_seconds":60}`, "missing_client_id"},
		{"no route_prefix", `{"client_id":"x","max_requests":10,"window_seconds":60}`, "missing_route_prefix"},
		{"zero max_requests", `{"client_id":"x","route_prefix":"x","max_requests":0,"window_seconds":60}`, "invalid_max_requests"},
		{"negative window", `{"client_id":"x","route_prefix":"x","max_requests":10,"window_seconds":-1}`, "invalid_window_seconds"},
		{"bad prefix", `{"client_id":"x","route_prefix":"BAD","max_requests":10,"window_seconds":60}`, "invalid_route_prefix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/policies",
				strings.NewReader(tt.body))
			req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400; body: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Errorf("body: got %q, want code %q", rec.Body.String(), tt.wantCode)
			}
		})
	}
}

func TestCreatePolicy_TakesEffectImmediately(t *testing.T) {
	// End-to-end test: create a policy, then verify the next request
	// through the proxy uses the new limit (proves cache invalidation).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, deps := newTestHandlerWithDeps(t)
	gatewayKey, clientID := registerClientAndGetID(t, deps, "effect-test")

	ctx := context.Background()
	if _, err := deps.Store.CreateRoute(ctx, "test", upstream.URL, []byte("x")); err != nil {
		t.Fatalf("create route: %v", err)
	}

	// First request — uses default (60/60). Triggers cache population.
	makeRequest := func() int {
		req := httptest.NewRequest(http.MethodGet, "/proxy/test/x", nil)
		req.Header.Set("X-Gateway-Key", gatewayKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := makeRequest(); code != http.StatusOK {
		t.Fatalf("warmup: got %d", code)
	}

	// Now create a policy with limit=1. This should invalidate the cache.
	body := strings.NewReader(fmt.Sprintf(
		`{"client_id":%q,"route_prefix":"test","max_requests":1,"window_seconds":60}`,
		clientID,
	))
	req := httptest.NewRequest(http.MethodPost, "/admin/policies", body)
	req.Header.Set("X-Admin-Key", deps.Config.AdminKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create policy: got %d", rec.Code)
	}

	// The next proxy request should see the new limit. The warmup call
	// already consumed 1 slot under the old default; under the new
	// limit=1 policy, the next request should be denied.
	if code := makeRequest(); code != http.StatusTooManyRequests {
		t.Errorf("after policy: got %d, want 429 (policy should be in effect)", code)
	}
}

// TestRateLimit_FullStackConcurrency is the headline correctness test for
// Phase 2. It exercises the entire authenticated-and-rate-limited proxy
// chain under concurrent load and asserts exactly max_requests of them
// succeed.
//
// This test demonstrates atomic correctness end-to-end:
//   - Auth middleware reads the same client from cache across goroutines
//   - Rate-limit middleware's policy lookup is consistent under contention
//   - The Lua script's check-and-increment is race-free
//   - The 429 responses have the right shape (Retry-After, X-RateLimit-*,
//     themed body with machine code)
//
// If this test ever produces "got N, want max_requests" where N drifts
// between runs, there's a race condition somewhere in the stack. The Lua
// atomicity test (in store_test.go) covers the Redis layer; this one
// covers everything above it.
func TestRateLimit_FullStackConcurrency(t *testing.T) {
	const (
		maxRequests   = 100
		totalRequests = 250
		concurrency   = 50
		windowSeconds = 60
	)

	// Fake upstream — instant 200s.
	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, deps := newTestHandlerWithDeps(t)
	ctx := context.Background()

	// Set up the world: client, route pointing at the fake upstream,
	// tight policy.
	gatewayKey, clientID := registerClientAndGetID(t, deps, "phase2-stress")

	if _, err := deps.Store.CreateRoute(ctx, "stress", upstream.URL, []byte("test-secret")); err != nil {
		t.Fatalf("create route: %v", err)
	}
	if _, err := deps.Store.CreateRateLimitPolicy(ctx, clientID, "stress", maxRequests, windowSeconds); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	// Fire totalRequests concurrent requests through the handler chain.
	var (
		allowed atomic.Int64
		denied  atomic.Int64
		errors  atomic.Int64

		// Capture one 429 response to verify shape.
		sample429Once sync.Once
		sample429Body string
		sample429Hdr  http.Header
	)

	var wg sync.WaitGroup
	jobs := make(chan int, totalRequests)
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				req := httptest.NewRequest(http.MethodGet, "/proxy/stress/x", nil)
				req.Header.Set("X-Gateway-Key", gatewayKey)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				switch rec.Code {
				case http.StatusOK:
					allowed.Add(1)
				case http.StatusTooManyRequests:
					denied.Add(1)
					sample429Once.Do(func() {
						sample429Body = rec.Body.String()
						sample429Hdr = rec.Header().Clone()
					})
				default:
					errors.Add(1)
					t.Errorf("unexpected status %d: body=%s", rec.Code, rec.Body.String())
				}
			}
		}()
	}

	for i := 0; i < totalRequests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// === Assertions ===

	gotAllowed := allowed.Load()
	gotDenied := denied.Load()
	gotErrors := errors.Load()

	if gotErrors != 0 {
		t.Fatalf("unexpected errors: %d (see above)", gotErrors)
	}

	if gotAllowed != int64(maxRequests) {
		t.Errorf("allowed: got %d, want %d (drift indicates a race condition)", gotAllowed, maxRequests)
	}
	if gotDenied != int64(totalRequests-maxRequests) {
		t.Errorf("denied: got %d, want %d", gotDenied, totalRequests-maxRequests)
	}

	// The upstream should have been hit exactly max_requests times
	// (denied requests never reach the upstream).
	if got := upstreamHits.Load(); got != int64(maxRequests) {
		t.Errorf("upstream hits: got %d, want %d (rate-limited requests should NOT reach upstream)",
			got, maxRequests)
	}

	// === Verify a single 429 has the right shape ===

	if sample429Body == "" {
		t.Fatal("no 429 captured — sample assertion impossible")
	}
	if !strings.Contains(sample429Body, `"code":"rate_limited"`) {
		t.Errorf("429 body missing code: %q", sample429Body)
	}
	if !strings.Contains(sample429Body, `"error":"the portcullis falls`) {
		t.Errorf("429 body missing themed message: %q", sample429Body)
	}
	if got := sample429Hdr.Get("Retry-After"); got == "" {
		t.Error("429 missing Retry-After header")
	}
	if got := sample429Hdr.Get("X-Ratelimit-Remaining"); got != "0" {
		t.Errorf("429 X-RateLimit-Remaining: got %q, want '0'", got)
	}
	if got := sample429Hdr.Get("X-Ratelimit-Limit"); got != "100" {
		t.Errorf("429 X-RateLimit-Limit: got %q, want '100'", got)
	}
}
