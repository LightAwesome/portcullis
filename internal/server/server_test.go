package server_test

import (
	"context"
	"encoding/json"
	"fmt"
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
