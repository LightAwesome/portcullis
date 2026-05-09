package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/LightAwesome/portcullis/internal/store"
	"github.com/LightAwesome/portcullis/internal/testutil"
)

var infra *testutil.Infra

func TestMain(m *testing.M) {
	ctx := context.Background()

	var err error
	infra, err = testutil.StartInfra(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test infra: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	infra.Stop(ctx)
	os.Exit(code)
}

// reset is a small wrapper for tests; equivalent to infra.Reset but
// localised here for readability.
func reset(t *testing.T) *store.Store {
	t.Helper()
	infra.Reset(t)
	return infra.Store
}

func TestCreateAndGetClient(t *testing.T) {
	db := reset(t)
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
		t.Errorf("name: got %q", created.Name)
	}

	fetched, err := db.GetClientByKeyID(ctx, "0123456789abcdef")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Name != "test-app" {
		t.Errorf("fetched name: got %q", fetched.Name)
	}
}

func TestGetClientByKeyID_NotFound(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	_, err := db.GetClientByKeyID(ctx, "doesnotexist00000")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateClient_DuplicateKeyID(t *testing.T) {
	db := reset(t)
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
	db := reset(t)
	ctx := context.Background()

	created, err := db.CreateRoute(ctx, "openai", "https://api.openai.com/v1", []byte("ciphertext"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Prefix != "openai" {
		t.Errorf("prefix: got %q", created.Prefix)
	}

	fetched, err := db.GetRouteByPrefix(ctx, "openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.TargetBaseURL != "https://api.openai.com/v1" {
		t.Errorf("target: got %q", fetched.TargetBaseURL)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	if err := db.CacheSet(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := db.CacheGet(ctx, "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "v" {
		t.Errorf("value: got %q, want %q", got, "v")
	}
}

func TestCacheGet_Miss(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	_, err := db.CacheGet(ctx, "nope")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCacheDel(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	if err := db.CacheSet(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := db.CacheDel(ctx, "k"); err != nil {
		t.Fatalf("del: %v", err)
	}
	_, err := db.CacheGet(ctx, "k")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after del, got %v", err)
	}
}

func TestIsolation(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	clients, err := db.ListClients(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(clients) != 0 {
		t.Errorf("expected empty after reset, got %d clients", len(clients))
	}
}
