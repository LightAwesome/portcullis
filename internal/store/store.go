// Package store owns all Postgres access for Portcullis.
//
// All callers receive domain types (Client, Route, ...) and typed errors
// (ErrNotFound, ErrConflict). pgx and pgxpool types do not leak past this
// package boundary.
//
// Concurrency: a *Store is safe for concurrent use. Construct one at startup
// (via New) and share it across all handlers and goroutines.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	MAX_CONNS = 25
	MIN_CONNS = 2
)

var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

type Store struct {
	pool  *pgxpool.Pool
	redis *redis.Client
}

func New(ctx context.Context, databaseURL string, redisURL string) (*Store, error) {

	pool, err := newPool(ctx, databaseURL)

	if err != nil {
		return nil, fmt.Errorf("postgres error: %w", err)
	}
	rdb, err := newRedis(ctx, redisURL)

	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("redis error: %w", err)
	}
	s := &Store{pool: pool, redis: rdb}

	// Preload Lua scripts so the first request doesn't pay the cache-miss
	// round trip. Best-effort: failure here is non-fatal because the script
	// runner falls back to EVAL transparently.
	if err := s.preloadRateLimitScript(ctx); err != nil {
		// Once we have logging in P3.1, this becomes a logger.Warn. For now,
		// silent — Run will EVAL on first call and self-heal.
		_ = err
	}

	return s, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func newPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)

	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.MaxConns = MAX_CONNS
	cfg.MinConns = MIN_CONNS

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

func newRedis(ctx context.Context, redisURL string) (*redis.Client, error) {

	opts, err := redis.ParseURL(redisURL)

	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	rdb := redis.NewClient(opts)

	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis cache: %w", err)
	}

	return rdb, nil

}

// TruncateAllForTesting wipes all gateway tables. ONLY for use in tests —
// the suffix is the convention; calling this from production code would
// destroy all data.
func (s *Store) TruncateAllForTesting(ctx context.Context) error {
	const q = `
		TRUNCATE
			request_logs,
			rate_limit_policies,
			upstream_routes,
			clients
		RESTART IDENTITY CASCADE
	`
	if _, err := s.pool.Exec(ctx, q); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	return nil
}

// FlushCacheForTesting wipes the Redis database. ONLY for tests.
func (s *Store) FlushCacheForTesting(ctx context.Context) error {
	if err := s.redis.FlushDB(ctx).Err(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}
