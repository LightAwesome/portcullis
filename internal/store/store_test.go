package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
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

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// Window of 60 seconds, limit of 3. First three calls should allow.
	for i := 0; i < 3; i++ {
		result, err := db.CheckRateLimit(ctx, "client1", "test", 3, 60, int64(1700000000000+i))
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !result.Allowed {
			t.Errorf("call %d: should be allowed", i)
		}
		expectedRemaining := int64(3 - i - 1)
		if result.Remaining != expectedRemaining {
			t.Errorf("call %d: remaining got %d, want %d", i, result.Remaining, expectedRemaining)
		}
	}
}

func TestRateLimit_DeniesOverLimit(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// Fill the bucket.
	for i := 0; i < 3; i++ {
		if _, err := db.CheckRateLimit(ctx, "client1", "test", 3, 60, int64(1700000000000+i)); err != nil {
			t.Fatalf("setup %d: %v", i, err)
		}
	}

	// Fourth should deny.
	result, err := db.CheckRateLimit(ctx, "client1", "test", 3, 60, 1700000000010)
	if err != nil {
		t.Fatalf("deny call: %v", err)
	}
	if result.Allowed {
		t.Error("fourth call should be denied")
	}
	if result.Remaining != 0 {
		t.Errorf("denied remaining: got %d, want 0", result.Remaining)
	}
}

func TestRateLimit_WindowResetsAfterExpiry(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// Fill the bucket at t=0.
	for i := 0; i < 3; i++ {
		if _, err := db.CheckRateLimit(ctx, "client1", "test", 3, 60, int64(1700000000000+i)); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Skip 70 seconds — past the 60s window.
	result, err := db.CheckRateLimit(ctx, "client1", "test", 3, 60, 1700000070000)
	if err != nil {
		t.Fatalf("post-window: %v", err)
	}
	if !result.Allowed {
		t.Error("post-window call should be allowed")
	}
	if result.Remaining != 2 {
		t.Errorf("post-window remaining: got %d, want 2", result.Remaining)
	}
}

func TestRateLimit_IsolatedByClient(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// client1 fills their bucket.
	for i := 0; i < 3; i++ {
		if _, err := db.CheckRateLimit(ctx, "client1", "test", 3, 60, int64(1700000000000+i)); err != nil {
			t.Fatalf("client1 setup: %v", err)
		}
	}

	// client2 should be unaffected.
	result, err := db.CheckRateLimit(ctx, "client2", "test", 3, 60, 1700000000010)
	if err != nil {
		t.Fatalf("client2 call: %v", err)
	}
	if !result.Allowed {
		t.Error("client2 should be allowed; their bucket is empty")
	}
	if result.Remaining != 2 {
		t.Errorf("client2 remaining: got %d, want 2", result.Remaining)
	}
}

func TestRateLimit_IsolatedByRoute(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// Fill the bucket on route "a".
	for i := 0; i < 3; i++ {
		if _, err := db.CheckRateLimit(ctx, "client1", "a", 3, 60, int64(1700000000000+i)); err != nil {
			t.Fatalf("route a: %v", err)
		}
	}

	// Route "b" should be unaffected.
	result, err := db.CheckRateLimit(ctx, "client1", "b", 3, 60, 1700000000010)
	if err != nil {
		t.Fatalf("route b: %v", err)
	}
	if !result.Allowed {
		t.Error("route b should be allowed; per-route bucket is independent")
	}
}

func TestRateLimit_ResetMSReflectsOldestEntry(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// First call at t=1000.
	result, err := db.CheckRateLimit(ctx, "client1", "test", 3, 60, 1700000001000)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	// The oldest (and only) entry's score is 1700000001000.
	// Window is 60s = 60000ms. So reset should be 1700000001000 + 60000 = 1700000061000.
	if result.ResetMS != 1700000061000 {
		t.Errorf("reset_ms: got %d, want 1700000061000", result.ResetMS)
	}
}

func TestRateLimit_AtomicUnderConcurrency(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	const (
		clientID   = "client1"
		prefix     = "test"
		maxReqs    = int64(100)
		windowSec  = int64(60)
		goroutines = 50
		perWorker  = 5
	)

	// Total requests = 50 * 5 = 250 against a limit of 100.
	// Expected: exactly 100 allowed, exactly 150 denied.

	now := time.Now().UnixMilli()

	var (
		allowed atomic.Int64
		denied  atomic.Int64
	)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// Each call gets a distinct nowMS so members don't collide
				// at the script level — but the script's own
				// uniqueness suffix would handle that anyway.
				ts := now + int64(g*perWorker+i)
				result, err := db.CheckRateLimit(ctx, clientID, prefix, maxReqs, windowSec, ts)
				if err != nil {
					t.Errorf("call: %v", err)
					return
				}
				if result.Allowed {
					allowed.Add(1)
				} else {
					denied.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()

	gotAllowed := allowed.Load()
	gotDenied := denied.Load()

	if gotAllowed != maxReqs {
		t.Errorf("allowed: got %d, want %d", gotAllowed, maxReqs)
	}
	if gotDenied != int64(goroutines*perWorker)-maxReqs {
		t.Errorf("denied: got %d, want %d", gotDenied, int64(goroutines*perWorker)-maxReqs)
	}
}

func TestGetRateLimitPolicy_FallsThroughToDefault(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// No policy registered. Should return the default.
	policy, err := db.GetRateLimitPolicy(ctx, "00000000-0000-0000-0000-000000000001", "no-such-route")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !policy.IsDefault {
		t.Error("policy should be marked as default")
	}
	if policy.MaxRequests != 60 || policy.WindowSeconds != 60 {
		t.Errorf("defaults: got %d/%d, want 60/60", policy.MaxRequests, policy.WindowSeconds)
	}
}

func TestCreateAndGetPolicy(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	keyHash := make([]byte, 32)
	client, err := db.CreateClient(ctx, "test-client", "0123456789abcdef", keyHash)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	clientID, _ := client.ID.Value()
	clientIDStr := clientID.(string)

	created, err := db.CreateRateLimitPolicy(ctx, clientIDStr, "openai", 100, 30)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if created.MaxRequests != 100 || created.WindowSeconds != 30 {
		t.Errorf("created: got %d/%d, want 100/30", created.MaxRequests, created.WindowSeconds)
	}

	got, err := db.GetRateLimitPolicy(ctx, clientIDStr, "openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.IsDefault {
		t.Error("policy should not be marked default")
	}
	if got.MaxRequests != 100 || got.WindowSeconds != 30 {
		t.Errorf("got: got %d/%d, want 100/30", got.MaxRequests, got.WindowSeconds)
	}
}

func TestCreatePolicy_UpsertSemantics(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	keyHash := make([]byte, 32)
	client, _ := db.CreateClient(ctx, "test-client", "0123456789abcdef", keyHash)
	clientID, _ := client.ID.Value()
	clientIDStr := clientID.(string)

	// First create: 100/30.
	if _, err := db.CreateRateLimitPolicy(ctx, clientIDStr, "openai", 100, 30); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Second "create" with different values: should update, not error.
	updated, err := db.CreateRateLimitPolicy(ctx, clientIDStr, "openai", 200, 60)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if updated.MaxRequests != 200 || updated.WindowSeconds != 60 {
		t.Errorf("upsert result: got %d/%d, want 200/60", updated.MaxRequests, updated.WindowSeconds)
	}

	got, err := db.GetRateLimitPolicy(ctx, clientIDStr, "openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.MaxRequests != 200 || got.WindowSeconds != 60 {
		t.Errorf("get after upsert: got %d/%d, want 200/60", got.MaxRequests, got.WindowSeconds)
	}
}

func TestCreatePolicy_UnknownClient(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// Valid-looking UUID, but no such client.
	_, err := db.CreateRateLimitPolicy(ctx, "00000000-0000-0000-0000-000000000000", "x", 10, 60)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound for unknown client, got %v", err)
	}
}

func TestInvalidatePolicyCache(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	keyHash := make([]byte, 32)
	client, _ := db.CreateClient(ctx, "test-client", "0123456789abcdef", keyHash)
	clientID, _ := client.ID.Value()
	clientIDStr := clientID.(string)

	// Look up a policy that doesn't exist — caches the default.
	_, err := db.GetRateLimitPolicy(ctx, clientIDStr, "openai")
	if err != nil {
		t.Fatalf("first get: %v", err)
	}

	// Confirm it's cached.
	if _, err := db.CacheGet(ctx, "policy:"+clientIDStr+":openai"); err != nil {
		t.Fatalf("expected cache hit: %v", err)
	}

	// Invalidate.
	if err := db.InvalidatePolicyCache(ctx, clientIDStr, "openai"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	// Confirm it's gone.
	_, err = db.CacheGet(ctx, "policy:"+clientIDStr+":openai")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected cache miss after invalidate, got %v", err)
	}
}
