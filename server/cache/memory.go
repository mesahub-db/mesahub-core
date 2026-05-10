// Package cache — in-process L1 cache and tiered (L1 → L2) wrapper.
//
// memClient is a standalone in-process cache backed by jellydator/ttlcache.
// It implements the same Client interface as redisClient but keeps all data
// in process memory with automatic TTL-based expiry and a background cleanup
// goroutine.
//
// tieredClient wraps any two Client implementations as L1 and L2:
//   - Reads: L1 hit → return. L1 miss → L2. If L2 hit, backfill L1.
//   - Writes: write to L1 then L2.
//   - Deletes: delete from both tiers.
//   - IncrRateLimit: delegates to L2 only (rate limiting must be global).
//   - Available: reports L2.Available() (L1 is always available in-process).
//
// Use NewTiered(l2) to build the wrapper with a fresh memClient as L1.
package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/singleflight"
)

// ── memClient ─────────────────────────────────────────────────────────────────

type memClient struct {
	apiKeys  *ttlcache.Cache[string, APIKeyValue]
	sessions *ttlcache.Cache[string, SessionValue]
	pkce     *ttlcache.Cache[string, PKCEValue]
	jsonData *ttlcache.Cache[string, []byte]
}

func newMemClient() *memClient {
	apiKeys := ttlcache.New[string, APIKeyValue](
		ttlcache.WithCapacity[string, APIKeyValue](50_000),
		ttlcache.WithDisableTouchOnHit[string, APIKeyValue](),
	)
	sessions := ttlcache.New[string, SessionValue](
		ttlcache.WithCapacity[string, SessionValue](50_000),
		ttlcache.WithDisableTouchOnHit[string, SessionValue](),
	)
	pkce := ttlcache.New[string, PKCEValue](
		ttlcache.WithCapacity[string, PKCEValue](10_000),
		ttlcache.WithDisableTouchOnHit[string, PKCEValue](),
	)
	jsonData := ttlcache.New[string, []byte](
		ttlcache.WithCapacity[string, []byte](10_000),
		ttlcache.WithDisableTouchOnHit[string, []byte](),
	)

	// Start background janitor goroutines for each cache.
	go apiKeys.Start()
	go sessions.Start()
	go pkce.Start()
	go jsonData.Start()

	return &memClient{
		apiKeys:  apiKeys,
		sessions: sessions,
		pkce:     pkce,
		jsonData: jsonData,
	}
}

func (m *memClient) Available() bool              { return true }
func (m *memClient) Ping(_ context.Context) error { return nil }

func (m *memClient) SetAPIKey(_ context.Context, hash string, v APIKeyValue, ttl time.Duration) error {
	m.apiKeys.Set(hash, v, ttl)
	return nil
}

func (m *memClient) GetAPIKey(_ context.Context, hash string) (*APIKeyValue, error) {
	item := m.apiKeys.Get(hash)
	if item == nil {
		return nil, nil
	}
	v := item.Value()
	return &v, nil
}

func (m *memClient) DeleteAPIKey(_ context.Context, hash string) error {
	m.apiKeys.Delete(hash)
	return nil
}

func (m *memClient) SetSession(_ context.Context, id string, v SessionValue, ttl time.Duration) error {
	m.sessions.Set(id, v, ttl)
	return nil
}

func (m *memClient) GetSession(_ context.Context, id string) (*SessionValue, error) {
	item := m.sessions.Get(id)
	if item == nil {
		return nil, nil
	}
	v := item.Value()
	return &v, nil
}

func (m *memClient) DeleteSession(_ context.Context, id string) error {
	m.sessions.Delete(id)
	return nil
}

func (m *memClient) SetPKCE(_ context.Context, state string, v PKCEValue, ttl time.Duration) error {
	m.pkce.Set(state, v, ttl)
	return nil
}

func (m *memClient) GetPKCE(_ context.Context, state string) (*PKCEValue, error) {
	item := m.pkce.Get(state)
	if item == nil {
		return nil, nil
	}
	v := item.Value()
	return &v, nil
}

func (m *memClient) DeletePKCE(_ context.Context, state string) error {
	m.pkce.Delete(state)
	return nil
}

func (m *memClient) IncrRateLimit(_ context.Context, _ string, _ time.Duration) (int64, error) {
	// Rate limiting is global — cannot be done per-process. Always delegate to L2.
	return 0, nil
}

func (m *memClient) SetJSON(_ context.Context, key string, v any, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.jsonData.Set(key, b, ttl)
	return nil
}

func (m *memClient) GetJSON(_ context.Context, key string, v any) (bool, error) {
	item := m.jsonData.Get(key)
	if item == nil {
		return false, nil
	}
	if err := json.Unmarshal(item.Value(), v); err != nil {
		return false, err
	}
	return true, nil
}

func (m *memClient) DeleteKey(_ context.Context, key string) error {
	// Try all sub-caches — only one will have the key.
	m.apiKeys.Delete(key)
	m.sessions.Delete(key)
	m.pkce.Delete(key)
	m.jsonData.Delete(key)
	return nil
}

// ── tieredClient ──────────────────────────────────────────────────────────────

// tieredClient wraps an in-process L1 (memClient) in front of any L2 Client.
//
// Singleflight groups prevent cache stampedes: if N goroutines concurrently
// miss L1 for the same key, only one L2 fetch is issued. The rest wait and
// share the result. One group per value type avoids cross-type key collisions.
type tieredClient struct {
	l1     *memClient
	l2     Client
	sfKey  singleflight.Group // API keys
	sfSess singleflight.Group // sessions
	sfPKCE singleflight.Group // PKCE
	sfJSON singleflight.Group // generic JSON
}

// l1TTL is how long any value is kept in the in-process L1 cache.
// L2 (Redis) uses the TTL supplied by each call site (typically longer).
const l1TTL = 5 * time.Minute

// NewTiered returns a Client that checks an in-process L1 cache (memClient)
// before delegating to l2 (Redis or noop). All writes go to both tiers;
// all deletes evict from both tiers. Rate limiting always goes to l2.
func NewTiered(l2 Client) Client {
	return &tieredClient{l1: newMemClient(), l2: l2}
}

func (t *tieredClient) Available() bool                { return t.l2.Available() }
func (t *tieredClient) Ping(ctx context.Context) error { return t.l2.Ping(ctx) }

// ── API keys ──────────────────────────────────────────────────────────────────

func (t *tieredClient) SetAPIKey(ctx context.Context, hash string, v APIKeyValue, ttl time.Duration) error {
	_ = t.l1.SetAPIKey(ctx, hash, v, l1TTL)
	return t.l2.SetAPIKey(ctx, hash, v, ttl)
}

func (t *tieredClient) GetAPIKey(ctx context.Context, hash string) (*APIKeyValue, error) {
	if v, err := t.l1.GetAPIKey(ctx, hash); v != nil || err != nil {
		return v, err
	}
	// Singleflight: collapse concurrent L2 fetches for the same key into one.
	type result struct{ v *APIKeyValue }
	v, err, _ := t.sfKey.Do(hash, func() (any, error) {
		v, err := t.l2.GetAPIKey(ctx, hash)
		if v != nil && err == nil {
			_ = t.l1.SetAPIKey(ctx, hash, *v, l1TTL)
		}
		return result{v}, err
	})
	if err != nil {
		return nil, err
	}
	return v.(result).v, nil
}

func (t *tieredClient) DeleteAPIKey(ctx context.Context, hash string) error {
	_ = t.l1.DeleteAPIKey(ctx, hash)
	return t.l2.DeleteAPIKey(ctx, hash)
}

// ── Sessions ──────────────────────────────────────────────────────────────────

func (t *tieredClient) SetSession(ctx context.Context, id string, v SessionValue, ttl time.Duration) error {
	_ = t.l1.SetSession(ctx, id, v, l1TTL)
	return t.l2.SetSession(ctx, id, v, ttl)
}

func (t *tieredClient) GetSession(ctx context.Context, id string) (*SessionValue, error) {
	if v, err := t.l1.GetSession(ctx, id); v != nil || err != nil {
		return v, err
	}
	type result struct{ v *SessionValue }
	v, err, _ := t.sfSess.Do(id, func() (any, error) {
		v, err := t.l2.GetSession(ctx, id)
		if v != nil && err == nil {
			_ = t.l1.SetSession(ctx, id, *v, l1TTL)
		}
		return result{v}, err
	})
	if err != nil {
		return nil, err
	}
	return v.(result).v, nil
}

func (t *tieredClient) DeleteSession(ctx context.Context, id string) error {
	_ = t.l1.DeleteSession(ctx, id)
	return t.l2.DeleteSession(ctx, id)
}

// ── PKCE ──────────────────────────────────────────────────────────────────────

func (t *tieredClient) SetPKCE(ctx context.Context, state string, v PKCEValue, ttl time.Duration) error {
	_ = t.l1.SetPKCE(ctx, state, v, l1TTL)
	return t.l2.SetPKCE(ctx, state, v, ttl)
}

func (t *tieredClient) GetPKCE(ctx context.Context, state string) (*PKCEValue, error) {
	if v, err := t.l1.GetPKCE(ctx, state); v != nil || err != nil {
		return v, err
	}
	type result struct{ v *PKCEValue }
	v, err, _ := t.sfPKCE.Do(state, func() (any, error) {
		v, err := t.l2.GetPKCE(ctx, state)
		if v != nil && err == nil {
			_ = t.l1.SetPKCE(ctx, state, *v, l1TTL)
		}
		return result{v}, err
	})
	if err != nil {
		return nil, err
	}
	return v.(result).v, nil
}

func (t *tieredClient) DeletePKCE(ctx context.Context, state string) error {
	_ = t.l1.DeletePKCE(ctx, state)
	return t.l2.DeletePKCE(ctx, state)
}

// ── Rate limiting ─────────────────────────────────────────────────────────────

func (t *tieredClient) IncrRateLimit(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	// Always delegate to L2 — rate limiting must be global across processes.
	return t.l2.IncrRateLimit(ctx, key, ttl)
}

// ── Generic JSON ──────────────────────────────────────────────────────────────

func (t *tieredClient) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	_ = t.l1.SetJSON(ctx, key, v, l1TTL)
	return t.l2.SetJSON(ctx, key, v, ttl)
}

func (t *tieredClient) GetJSON(ctx context.Context, key string, v any) (bool, error) {
	if hit, err := t.l1.GetJSON(ctx, key, v); hit || err != nil {
		return hit, err
	}
	// Use singleflight to collapse concurrent L2 fetches.
	// We fetch raw JSON bytes from L2 so we can share the result across callers
	// and unmarshal independently into each caller's v.
	type result struct{ raw []byte }
	shared, err, _ := t.sfJSON.Do(key, func() (any, error) {
		var tmp map[string]any
		hit, err := t.l2.GetJSON(ctx, key, &tmp)
		if !hit || err != nil {
			return result{}, err
		}
		b, err := json.Marshal(tmp)
		if err != nil {
			return result{}, err
		}
		// Backfill L1 from raw bytes.
		var backfill map[string]any
		_ = json.Unmarshal(b, &backfill)
		_ = t.l1.SetJSON(ctx, key, backfill, l1TTL)
		return result{b}, nil
	})
	if err != nil {
		return false, err
	}
	b := shared.(result).raw
	if len(b) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, err
	}
	return true, nil
}

func (t *tieredClient) DeleteKey(ctx context.Context, key string) error {
	_ = t.l1.DeleteKey(ctx, key)
	return t.l2.DeleteKey(ctx, key)
}
