package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/LightAwesome/portcullis/internal/store"
)

// newTestStore spins up a fresh Postgres container, applies migrations,
// and returns a connected Store. The container is torn down at test end.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()

	ctx := context.Background()

	// Locate migrations relative to this test file. runtime.Caller is the
	// idiomatic way to find paths relative to the source file at test time.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")

	container, err := postgres.Run(ctx,
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
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	db, err := store.New(ctx, connStr)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(db.Close)

	return db
}

func TestCreateAndGetClient(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	keyHash := make([]byte, 32)
	for i := range keyHash {
		keyHash[i] = byte(i)
	}

	created, err := db.CreateClient(ctx, "test-app", "0123456789abcdef", keyHash)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "test-app" {
		t.Errorf("name: got %q, want %q", created.Name, "test-app")
	}

	fetched, err := db.GetClientByKeyID(ctx, "0123456789abcdef")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Name != "test-app" {
		t.Errorf("fetched name: got %q, want %q", fetched.Name, "test-app")
	}
}

func TestGetClientByKeyID_NotFound(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	_, err := db.GetClientByKeyID(ctx, "doesnotexist00000")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateClient_DuplicateKeyID(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	keyHash := make([]byte, 32)
	if _, err := db.CreateClient(ctx, "first", "0123456789abcdef", keyHash); err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err := db.CreateClient(ctx, "second", "0123456789abcdef", keyHash)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestCreateAndGetRoute(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	created, err := db.CreateRoute(ctx, "openai", "https://api.openai.com/v1", []byte("ciphertext"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Prefix != "openai" {
		t.Errorf("prefix: got %q, want %q", created.Prefix, "openai")
	}

	fetched, err := db.GetRouteByPrefix(ctx, "openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.TargetBaseURL != "https://api.openai.com/v1" {
		t.Errorf("target: got %q, want %q", fetched.TargetBaseURL, "https://api.openai.com/v1")
	}
}
