package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Route is an upstream API the gateway proxies to.
type Route struct {
	ID                       pgtype.UUID
	Prefix                   string
	TargetBaseURL            string
	UpstreamSecretCiphertext []byte
	IsActive                 bool
	CreatedAt                time.Time
}

// CreateRoute inserts a new upstream route. The upstream secret should be
// passed as ciphertext (encryption happens above this layer in Phase 4;
// for now it can be plaintext bytes).
//
// Returns ErrConflict if prefix already exists.
func (s *Store) CreateRoute(ctx context.Context, prefix, targetBaseURL string, secretCiphertext []byte) (*Route, error) {
	const q = `
		INSERT INTO upstream_routes (prefix, target_base_url, upstream_secret_ciphertext)
		VALUES ($1, $2, $3)
		RETURNING id, prefix, target_base_url, upstream_secret_ciphertext, is_active, created_at
	`
	row := s.pool.QueryRow(ctx, q, prefix, targetBaseURL, secretCiphertext)

	var r Route
	if err := row.Scan(&r.ID, &r.Prefix, &r.TargetBaseURL, &r.UpstreamSecretCiphertext, &r.IsActive, &r.CreatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create route: %w", err)
	}
	return &r, nil
}

// GetRouteByPrefix returns a route by its URL prefix. Called on every
// proxied request.
//
// Returns ErrNotFound if no row matches.
func (s *Store) GetRouteByPrefix(ctx context.Context, prefix string) (*Route, error) {
	const q = `
		SELECT id, prefix, target_base_url, upstream_secret_ciphertext, is_active, created_at
		FROM upstream_routes
		WHERE prefix = $1
	`
	row := s.pool.QueryRow(ctx, q, prefix)

	var r Route
	if err := row.Scan(&r.ID, &r.Prefix, &r.TargetBaseURL, &r.UpstreamSecretCiphertext, &r.IsActive, &r.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get route by prefix: %w", err)
	}
	return &r, nil
}

// ListRoutes returns all upstream routes, alphabetical by prefix.
func (s *Store) ListRoutes(ctx context.Context) ([]*Route, error) {
	const q = `
		SELECT id, prefix, target_base_url, upstream_secret_ciphertext, is_active, created_at
		FROM upstream_routes
		ORDER BY prefix
	`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	defer rows.Close()

	var routes []*Route
	for rows.Next() {
		var r Route
		if err := rows.Scan(&r.ID, &r.Prefix, &r.TargetBaseURL, &r.UpstreamSecretCiphertext, &r.IsActive, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan route: %w", err)
		}
		routes = append(routes, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate routes: %w", err)
	}
	return routes, nil
}
