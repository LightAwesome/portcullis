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
			Env:      config.EnvDevelopment,
			AdminKey: testAdminKey,
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
