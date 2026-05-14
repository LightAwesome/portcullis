// Package testutil provides shared helpers for tests that need real
// infrastructure (Postgres, Redis).
//
// Designed to be called from a package's TestMain, with the returned
// resources stored in package-level variables for reuse across tests.
package testutil

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/LightAwesome/portcullis/internal/store"
)

// Infra bundles the testcontainer-backed Postgres and Redis plus a connected
// store. Construct via StartInfra, store in a package-level var, and Reset
// between tests.
type Infra struct {
	Store       *store.Store
	postgresCt  *postgres.PostgresContainer
	redisCt     testcontainers.Container
	postgresURL string
	redisURL    string
}

// StartInfra brings up Postgres and Redis containers, applies migrations,
// and returns a connected Store. Caller is responsible for invoking Stop
// in their TestMain teardown.
//
// migrationsDir is resolved relative to the caller's source file via
// runtime.Caller; pass "" to auto-detect from the caller's location.
func StartInfra(ctx context.Context) (*Infra, error) {
	// Find the migrations directory relative to this file. testutil lives
	// at internal/testutil, migrations at the repo root, so it's two ups.
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
		return nil, fmt.Errorf("start postgres: %w", err)
	}

	rdContainer, err := redis.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		return nil, fmt.Errorf("start redis: %w", err)
	}

	pgURL, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = rdContainer.Terminate(ctx)
		return nil, fmt.Errorf("postgres conn string: %w", err)
	}
	rdURL, err := rdContainer.ConnectionString(ctx)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = rdContainer.Terminate(ctx)
		return nil, fmt.Errorf("redis conn string: %w", err)
	}

	db, err := store.New(ctx, pgURL, rdURL, 60, 60)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = rdContainer.Terminate(ctx)
		return nil, fmt.Errorf("open store: %w", err)
	}

	return &Infra{
		Store:       db,
		postgresCt:  pgContainer,
		redisCt:     rdContainer,
		postgresURL: pgURL,
		redisURL:    rdURL,
	}, nil
}

// Stop closes the store and terminates the containers. Call from TestMain
// teardown.
func (i *Infra) Stop(ctx context.Context) {
	i.Store.Close()
	_ = i.postgresCt.Terminate(ctx)
	_ = i.redisCt.Terminate(ctx)
}

// Reset truncates all gateway tables and flushes Redis, returning the
// store to a clean state for the next test.
//
// Call at the start of any test that mutates state. Tests that only read
// don't strictly need Reset, but it's cheap and avoids ordering surprises.
func (i *Infra) Reset(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if err := i.Store.TruncateAllForTesting(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := i.Store.FlushCacheForTesting(ctx); err != nil {
		t.Fatalf("flush redis: %v", err)
	}
}
