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

// Client is a registered application that authenticates to the gateway.
type Client struct {
	ID        pgtype.UUID
	Name      string
	KeyID     string
	KeyHash   []byte
	IsActive  bool
	CreatedAt time.Time
}

// CreateClient inserts a new client. Caller provides the keyID and HMAC of
// the secret (the secret itself is never seen by store).
//
// Returns ErrConflict if keyID already exists.
func (s *Store) CreateClient(ctx context.Context, name, keyID string, keyHash []byte) (*Client, error) {
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

// GetClientByKeyID returns a client by their key_id. Hot path — called on
// every authenticated request.
//
// Returns ErrNotFound if no row matches.
func (s *Store) GetClientByKeyID(ctx context.Context, keyID string) (*Client, error) {
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
