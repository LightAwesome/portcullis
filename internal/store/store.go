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
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
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

	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
