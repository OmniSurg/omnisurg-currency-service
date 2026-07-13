// Package cache wraps a redis-backed latest-rate cache. Every method degrades
// gracefully: a nil client or any redis error is treated as a cache miss so the
// service always falls back to the database. Conversion and rate reads never
// fail because redis is down.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
)

// RateCache caches the latest snapshot per currency pair.
type RateCache struct {
	client *redis.Client
	ttl    time.Duration
}

// New builds a RateCache. A nil client disables caching (every op is a miss).
func New(client *redis.Client, ttl time.Duration) *RateCache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &RateCache{client: client, ttl: ttl}
}

// cachedRate is the redis JSON value. Rate is a decimal string to avoid float
// drift on the round trip.
type cachedRate struct {
	ID         string    `json:"id"`
	Base       string    `json:"base"`
	Quote      string    `json:"quote"`
	Rate       string    `json:"rate"`
	Source     string    `json:"source"`
	CapturedAt time.Time `json:"captured_at"`
}

func key(base, quote string) string { return fmt.Sprintf("fx:latest:%s:%s", base, quote) }

// Get returns the cached snapshot and true on a hit. Any error or nil client is
// a miss, never an error to the caller.
func (c *RateCache) Get(ctx context.Context, base, quote string) (model.FXSnapshot, bool) {
	if c == nil || c.client == nil {
		return model.FXSnapshot{}, false
	}
	raw, err := c.client.Get(ctx, key(base, quote)).Bytes()
	if err != nil {
		return model.FXSnapshot{}, false
	}
	var cr cachedRate
	if jerr := json.Unmarshal(raw, &cr); jerr != nil {
		return model.FXSnapshot{}, false
	}
	rate, derr := decimal.NewFromString(cr.Rate)
	if derr != nil {
		return model.FXSnapshot{}, false
	}
	return model.FXSnapshot{
		Base: cr.Base, Quote: cr.Quote, Rate: rate,
		Source: cr.Source, CapturedAt: cr.CapturedAt,
	}, true
}

// Set stores a snapshot under the pair key with the configured TTL. Errors are
// swallowed: a failed cache write must not fail the request.
func (c *RateCache) Set(ctx context.Context, s model.FXSnapshot) {
	if c == nil || c.client == nil {
		return
	}
	payload, err := json.Marshal(cachedRate{
		ID: s.ID.String(), Base: s.Base, Quote: s.Quote,
		Rate: s.Rate.String(), Source: s.Source, CapturedAt: s.CapturedAt,
	})
	if err != nil {
		return
	}
	_ = c.client.Set(ctx, key(s.Base, s.Quote), payload, c.ttl).Err()
}

// Invalidate removes the cached entry for a pair, used after a refresh or manual
// override so the next read sees the new snapshot immediately.
func (c *RateCache) Invalidate(ctx context.Context, base, quote string) {
	if c == nil || c.client == nil {
		return
	}
	_ = c.client.Del(ctx, key(base, quote)).Err()
}
