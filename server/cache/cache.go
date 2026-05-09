// Package cache provides pluggable cache backends for the template server.
//
// Two modes are supported:
//
//   - "off"   — NoopClient; all operations silently no-op. API keys hit
//     SQLite on every request. Rate limiting is always a no-op.
//   - "redis" — Redis-backed (requires REDIS_URL). API keys are cached with
//     a configurable TTL. Rate limiting uses atomic Redis INCR.
//
// Mode is derived automatically: "redis" when REDIS_URL is set, "off"
// otherwise. The factory function New() picks the right implementation.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache mode constants.
const (
	ModeOff   = "off"
	ModeRedis = "redis"
)

// SessionValue holds the data stored per session.
type SessionValue struct {
	Role   string `json:"role"`
	UserID string `json:"user_id,omitempty"`
	Email  string `json:"email,omitempty"`
}

// APIKeyValue holds the data cached per hashed API key.
// Scopes is a JSON-encoded array of permission strings using the format
// "<type>:<target>:<level>" where level is "r" (read-only) or "w" (read+write).
// Examples: ["all:w"], ["db:*:r"], ["db:D-abc-mydb:w"], ["bucket:B-abc-photos:r"]
type APIKeyValue struct {
	UserID string   `json:"user_id"`
	KeyID  string   `json:"key_id"`
	Owner  string   `json:"owner"`
	Scopes []string `json:"scopes"`
}

// PKCEValue holds PKCE state for the OAuth flow.
type PKCEValue struct {
	Verifier    string `json:"verifier"`
	RedirectURI string `json:"redirect_uri"`
	ReturnTo    string `json:"return_to,omitempty"`
}

// Client is the interface every cache backend implements.
type Client interface {
	SetSession(ctx context.Context, id string, v SessionValue, ttl time.Duration) error
	GetSession(ctx context.Context, id string) (*SessionValue, error)
	DeleteSession(ctx context.Context, id string) error

	SetAPIKey(ctx context.Context, hash string, v APIKeyValue, ttl time.Duration) error
	GetAPIKey(ctx context.Context, hash string) (*APIKeyValue, error)
	// DeleteAPIKey immediately evicts a cached API key. Called by template's
	// internal endpoint when control notifies it of a revocation.
	DeleteAPIKey(ctx context.Context, hash string) error

	SetPKCE(ctx context.Context, state string, v PKCEValue, ttl time.Duration) error
	GetPKCE(ctx context.Context, state string) (*PKCEValue, error)
	// DeletePKCE removes a PKCE state entry after the OAuth callback consumes it,
	// preventing replay attacks within the TTL window.
	DeletePKCE(ctx context.Context, state string) error

	IncrRateLimit(ctx context.Context, key string, ttl time.Duration) (int64, error)

	// SetJSON encodes v as JSON and stores it under key with the given TTL.
	// Used by callers that need to cache arbitrary typed values (e.g. db/bucket lists).
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
	// GetJSON reads the cached value for key and unmarshals it into v.
	// Returns (true, nil) on a hit, (false, nil) on a miss, (false, err) on error.
	GetJSON(ctx context.Context, key string, v any) (bool, error)
	// DeleteKey removes an arbitrary cache key. Used for targeted invalidation.
	DeleteKey(ctx context.Context, key string) error

	// Ping checks connectivity; always returns nil for NoopClient.
	Ping(ctx context.Context) error
	// Available reports whether a real Redis connection is backing this client.
	Available() bool
}

// ── NoopClient ───────────────────────────────────────────────────────────────

type noopClient struct{}

// NewNoop returns a NoopClient. All writes are discarded; reads return nil.
func NewNoop() Client { return &noopClient{} }

func (n *noopClient) SetSession(_ context.Context, _ string, _ SessionValue, _ time.Duration) error {
	return nil
}
func (n *noopClient) GetSession(_ context.Context, _ string) (*SessionValue, error) { return nil, nil }
func (n *noopClient) DeleteSession(_ context.Context, _ string) error               { return nil }
func (n *noopClient) SetAPIKey(_ context.Context, _ string, _ APIKeyValue, _ time.Duration) error {
	return nil
}
func (n *noopClient) GetAPIKey(_ context.Context, _ string) (*APIKeyValue, error) { return nil, nil }
func (n *noopClient) DeleteAPIKey(_ context.Context, _ string) error              { return nil }
func (n *noopClient) SetPKCE(_ context.Context, _ string, _ PKCEValue, _ time.Duration) error {
	return nil
}
func (n *noopClient) GetPKCE(_ context.Context, _ string) (*PKCEValue, error) { return nil, nil }
func (n *noopClient) DeletePKCE(_ context.Context, _ string) error            { return nil }
func (n *noopClient) IncrRateLimit(_ context.Context, _ string, _ time.Duration) (int64, error) {
	return 0, nil
}
func (n *noopClient) SetJSON(_ context.Context, _ string, _ any, _ time.Duration) error { return nil }
func (n *noopClient) GetJSON(_ context.Context, _ string, _ any) (bool, error)          { return false, nil }
func (n *noopClient) DeleteKey(_ context.Context, _ string) error                       { return nil }
func (n *noopClient) Ping(_ context.Context) error                                      { return nil }
func (n *noopClient) Available() bool                                                   { return false }

// ── RedisClient ──────────────────────────────────────────────────────────────

type redisClient struct {
	rdb *redis.Client
}

// NewRedis connects to Redis and returns a Client. Returns an error if the
// initial PING fails (fail-fast so misconfigured deployments are obvious).
func NewRedis(redisURL string) (Client, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("cache: invalid REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("cache: redis ping failed: %w", err)
	}
	return &redisClient{rdb: rdb}, nil
}

func (r *redisClient) Available() bool { return true }

func (r *redisClient) Ping(ctx context.Context) error {
	return r.rdb.Ping(ctx).Err()
}

func (r *redisClient) SetSession(ctx context.Context, id string, v SessionValue, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.rdb.Set(ctx, "MH::sh:session:"+id, b, ttl).Err()
}

func (r *redisClient) GetSession(ctx context.Context, id string) (*SessionValue, error) {
	b, err := r.rdb.Get(ctx, "MH::sh:session:"+id).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v SessionValue
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *redisClient) DeleteSession(ctx context.Context, id string) error {
	return r.rdb.Del(ctx, "MH::sh:session:"+id).Err()
}

func (r *redisClient) SetAPIKey(ctx context.Context, hash string, v APIKeyValue, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.rdb.Set(ctx, "MH::sh:apikey:"+hash, b, ttl).Err()
}

func (r *redisClient) GetAPIKey(ctx context.Context, hash string) (*APIKeyValue, error) {
	b, err := r.rdb.Get(ctx, "MH::sh:apikey:"+hash).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v APIKeyValue
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *redisClient) SetPKCE(ctx context.Context, state string, v PKCEValue, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.rdb.Set(ctx, "MH::sh:pkce:"+state, b, ttl).Err()
}

func (r *redisClient) GetPKCE(ctx context.Context, state string) (*PKCEValue, error) {
	b, err := r.rdb.Get(ctx, "MH::sh:pkce:"+state).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v PKCEValue
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *redisClient) DeleteAPIKey(ctx context.Context, hash string) error {
	return r.rdb.Del(ctx, "MH::sh:apikey:"+hash).Err()
}

func (r *redisClient) DeletePKCE(ctx context.Context, state string) error {
	return r.rdb.Del(ctx, "MH::sh:pkce:"+state).Err()
}

func (r *redisClient) IncrRateLimit(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := r.rdb.Pipeline()
	incr := pipe.Incr(ctx, "MH::sh:ratelimit:"+key)
	pipe.Expire(ctx, "MH::sh:ratelimit:"+key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

func (r *redisClient) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.rdb.Set(ctx, key, b, ttl).Err()
}

func (r *redisClient) GetJSON(ctx context.Context, key string, v any) (bool, error) {
	b, err := r.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, err
	}
	return true, nil
}

func (r *redisClient) DeleteKey(ctx context.Context, key string) error {
	return r.rdb.Del(ctx, key).Err()
}

// ── Factory ───────────────────────────────────────────────────────────────────

// New returns a cache Client. If redisURL is non-empty a Redis-backed L2 is
// used; otherwise a no-op L2 is used. Either way the result is wrapped in a
// tieredClient so hot data (API keys, sessions) is served from in-process
// memory (L1) without a Redis round-trip on every request.
func New(redisURL string) (Client, error) {
	var l2 Client
	var err error
	if redisURL != "" {
		l2, err = NewRedis(redisURL)
		if err != nil {
			return nil, err
		}
	} else {
		l2 = &noopClient{}
	}
	return NewTiered(l2), nil
}
