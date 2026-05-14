package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RateLimitPolicy is the resolved rate-limit policy for a (client, route) pair.
//
// Always populated — GetRateLimitPolicy never returns "not found"; it
// returns the global default when no row exists.
type RateLimitPolicy struct {
	ClientID      string `json:"client_id"`
	RoutePrefix   string `json:"route_prefix"`
	MaxRequests   int    `json:"max_requests"`
	WindowSeconds int    `json:"window_seconds"`
	IsDefault     bool   `json:"is_default"`
}

const (
	policyCacheKeyPrefix = "policy:"
	policyCacheTTL       = 5 * time.Minute
)

func policyCacheKey(clientID, routePrefix string) string {
	return policyCacheKeyPrefix + clientID + ":" + routePrefix
}

// CreateRateLimitPolicy inserts or updates a per-client per-route policy.
//
// Uses INSERT ... ON CONFLICT to handle upsert semantics: callers don't
// need to differentiate "create new" from "update existing." Returns the
// stored policy.
//
// Invalidates the cached policy after the write so subsequent lookups
// see the new values without waiting for TTL.
func (s *Store) CreateRateLimitPolicy(ctx context.Context, clientID, routePrefix string, maxRequests, windowSeconds int) (*RateLimitPolicy, error) {
	if maxRequests <= 0 {
		return nil, errors.New("max_requests must be positive")
	}
	if windowSeconds <= 0 {
		return nil, errors.New("window_seconds must be positive")
	}

	const q = `
		INSERT INTO rate_limit_policies (client_id, route_prefix, max_requests, window_seconds)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (client_id, route_prefix) DO UPDATE
		SET max_requests = EXCLUDED.max_requests,
		    window_seconds = EXCLUDED.window_seconds
		RETURNING client_id, route_prefix, max_requests, window_seconds
	`

	var p RateLimitPolicy
	row := s.pool.QueryRow(ctx, q, clientID, routePrefix, maxRequests, windowSeconds)
	if err := row.Scan(&p.ClientID, &p.RoutePrefix, &p.MaxRequests, &p.WindowSeconds); err != nil {
		// Foreign key violation = no such client.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("create policy: %w", err)
	}

	// Invalidate cache. Non-fatal on failure — TTL eventually clears stale.
	_ = s.InvalidatePolicyCache(ctx, clientID, routePrefix)

	return &p, nil
}

// GetRateLimitPolicy returns the policy applicable to (clientID, routePrefix).
//
// Lookup order:
//  1. Redis cache (cache-aside)
//  2. Postgres
//  3. Global default (from store config)
//
// The default is cached like any other answer, with the IsDefault flag set.
// Returning a default never produces ErrNotFound — callers receive a usable
// policy in all cases.
func (s *Store) GetRateLimitPolicy(ctx context.Context, clientID, routePrefix string) (*RateLimitPolicy, error) {
	if p, err := s.getPolicyFromCache(ctx, clientID, routePrefix); err == nil {
		return p, nil
	}

	const q = `
		SELECT client_id, route_prefix, max_requests, window_seconds
		FROM rate_limit_policies
		WHERE client_id = $1 AND route_prefix = $2
	`

	var p RateLimitPolicy
	row := s.pool.QueryRow(ctx, q, clientID, routePrefix)
	err := row.Scan(&p.ClientID, &p.RoutePrefix, &p.MaxRequests, &p.WindowSeconds)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Fall through to default.
		p = RateLimitPolicy{
			ClientID:      clientID,
			RoutePrefix:   routePrefix,
			MaxRequests:   s.defaultMaxRequests,
			WindowSeconds: s.defaultWindowSeconds,
			IsDefault:     true,
		}
	case err != nil:
		return nil, fmt.Errorf("get policy: %w", err)
	}

	_ = s.cachePolicy(ctx, &p)
	return &p, nil
}

// InvalidatePolicyCache removes the cached policy for (clientID, routePrefix).
//
// Admin handlers that create, update, or delete a policy call this after
// the database write so subsequent lookups don't see stale data.
func (s *Store) InvalidatePolicyCache(ctx context.Context, clientID, routePrefix string) error {
	return s.CacheDel(ctx, policyCacheKey(clientID, routePrefix))
}

func (s *Store) getPolicyFromCache(ctx context.Context, clientID, routePrefix string) (*RateLimitPolicy, error) {
	b, err := s.CacheGet(ctx, policyCacheKey(clientID, routePrefix))
	if err != nil {
		return nil, err
	}
	var p RateLimitPolicy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("decode cached policy: %w", err)
	}
	return &p, nil
}

func (s *Store) cachePolicy(ctx context.Context, p *RateLimitPolicy) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.CacheSet(ctx, policyCacheKey(p.ClientID, p.RoutePrefix), b, policyCacheTTL)
}
