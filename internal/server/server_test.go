package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/LightAwesome/portcullis/internal/config"
	"github.com/LightAwesome/portcullis/internal/server"
	"github.com/LightAwesome/portcullis/internal/store"
)

// newTestServer spins up Postgres + Redis containers, builds a Dependencies
// against them, and returns the HTTP handler ready to be hit with httptest.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	ctx := context.Background()

	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")

	pgContainer, err := postgres.Run(ctx,
		"postgres:16.4-alpine",
		postgres.WithDatabase("portcullis_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
		postgres.WithInitScripts(filepath.Join(migrationsDir, "0001_init.up.sql")),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgContainer.Terminate(ctx) })

	rdContainer, err := redis.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = rdContainer.Terminate(ctx) })

	pgURL, _ := pgContainer.ConnectionString(ctx, "sslmode=disable")
	rdURL, _ := rdContainer.ConnectionString(ctx)

	db, err := store.New(ctx, pgURL, rdURL)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(db.Close)

	deps := &server.Dependencies{
		Config: &config.Config{Env: config.EnvDevelopment},
		Store:  db,
	}
	return server.NewServer(deps)
}

func TestHealth_OK(t *testing.T) {
	handler := newTestServer(t)

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
		t.Errorf("postgres: got %q, want %q", body.Postgres, "ok")
	}
	if body.Redis != "ok" {
		t.Errorf("redis: got %q, want %q", body.Redis, "ok")
	}
}

func TestHealth_ServeMethodIsGet(t *testing.T) {
	handler := newTestServer(t)

	// POST to /health should be 405 Method Not Allowed (Chi's default).
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to /health: got %d, want 405", rec.Code)
	}
}
