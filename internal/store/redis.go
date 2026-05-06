package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CacheGet retrieves a value from Redis by key.
//
// Returns ErrNotFound on cache miss; the redis.Nil sentinel is translated
// at the boundary so callers can use errors.Is(err, store.ErrNotFound).
func (s *Store) CacheGet(ctx context.Context, key string) ([]byte, error) {
	b, err := s.redis.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cache get: %w", err)
	}
	return b, nil
}

// CacheSet stores a value with the given TTL.
//
// Pass ttl=0 to store without expiry, though for our use case every cached
// value should have a TTL — Redis is for ephemeral state, never source of truth.
func (s *Store) CacheSet(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := s.redis.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache set: %w", err)
	}
	return nil
}

// CacheDel removes one or more keys. Missing keys are silently ignored.
func (s *Store) CacheDel(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := s.redis.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cache del: %w", err)
	}
	return nil
}
