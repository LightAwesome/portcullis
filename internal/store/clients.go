package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Client is a registered application that authenticates to the gateway.
type Client struct {
	ID        pgtype.UUID
	Name      string
	KeyID     string
	KeyHash   []byte
	IsActive  bool
	CreatedAt time.Time
}

const (
	clientCacheKeyPrefix = "client:"
	clientCacheTTL       = 60 * time.Second
)

func clientCacheKey(keyID string) string {
	return clientCacheKeyPrefix + keyID
}

// CreateClient inserts a new client. Caller provides the keyID and HMAC of
// the secret (the secret itself is never seen by store).
//
// Returns ErrConflict if keyID already exists.
func (s *Store) CreateClient(ctx context.Context, name, keyID string, keyHash []byte) (*Client, error) {
	if c, err := s.getClientFromCache(ctx, keyID); err == nil {
		return c, nil
	}
	const q = `
		INSERT INTO clients (name, key_id, key_hash)
		VALUES ($1, $2, $3)
		RETURNING id, name, key_id, key_hash, is_active, created_at
	`
	row := s.pool.QueryRow(ctx, q, name, keyID, keyHash)

	var c Client
	if err := row.Scan(&c.ID, &c.Name, &c.KeyID, &c.KeyHash, &c.IsActive, &c.CreatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create client: %w", err)
	}
	return &c, nil
}

// GetClientByKeyID returns a client by their key_id, using cache-aside.
//
// Hot path — called on every authenticated request. Tries Redis first; on
// miss, queries Postgres and populates the cache. Cache failures are
// non-fatal: a Redis outage degrades to direct-Postgres reads, not auth
// failure.
//
// Negative results are NOT cached: an unknown keyID always falls through
// to Postgres. See PRD §4.2 — attackers spamming invalid keys are
// addressed by rate limiting, not negative caching.
//
// Returns ErrNotFound if no row matches.
func (s *Store) GetClientByKeyID(ctx context.Context, keyID string) (*Client, error) {
	if c, err := s.getClientFromCache(ctx, keyID); err == nil {
		return c, nil
	}
	const q = `
		SELECT id, name, key_id, key_hash, is_active, created_at
		FROM clients
		WHERE key_id = $1
	`
	row := s.pool.QueryRow(ctx, q, keyID)

	var c Client
	if err := row.Scan(&c.ID, &c.Name, &c.KeyID, &c.KeyHash, &c.IsActive, &c.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get client by key_id: %w", err)
	}

	_ = s.cacheClient(ctx, &c)

	return &c, nil
}

// ListClients returns all registered clients, newest first.
func (s *Store) ListClients(ctx context.Context) ([]*Client, error) {
	const q = `
		SELECT id, name, key_id, key_hash, is_active, created_at
		FROM clients
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	defer rows.Close()

	var clients []*Client
	for rows.Next() {
		var c Client
		if err := rows.Scan(&c.ID, &c.Name, &c.KeyID, &c.KeyHash, &c.IsActive, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan client: %w", err)
		}
		clients = append(clients, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate clients: %w", err)
	}
	return clients, nil
}

// getClientFromCache reads and decodes a client from Redis.
// Returns ErrNotFound on cache miss; any other error indicates a Redis problem.
func (s *Store) getClientFromCache(ctx context.Context, keyID string) (*Client, error) {

	b, err := s.CacheGet(ctx, keyID)

	if err != nil {
		return nil, fmt.Errorf("cache miss: %w", err)

	}
	var c Client

	if err := json.Unmarshal(b, &c); err != nil {
		// Corrupt cache entry — treat as miss and force Postgres re-fetch.
		// A persistent corruption issue would surface in Postgres-load metrics.
		return nil, fmt.Errorf("decode cached client: %w", err)
	}
	return &c, nil

}

// cacheClient() takes the client and sets the cache with the TTL. It uses the clientCacheKey func to append the client Prefix contstant to the key ID, and store the JSON encoded byte stream in the cache
func (s *Store) cacheClient(ctx context.Context, c *Client) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	s.CacheSet(ctx, clientCacheKey(c.KeyID), b, clientCacheTTL)
	return nil
}

// InvalidateClientCache removes the cached entry for a client by keyID.
//
// Admin endpoints that mutate clients (deactivate, delete, rename) call this
// after the database write so subsequent auth lookups don't see stale data.
// Errors are non-fatal — a stale cache entry expires within clientCacheTTL.
func (s *Store) InvalidateClientCache(ctx context.Context, keyID string) error {
	return s.CacheDel(ctx, clientCacheKey(keyID))
}
