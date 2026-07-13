package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/cache"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func snap() model.FXSnapshot {
	r, _ := decimal.NewFromString("26.7692")
	return model.FXSnapshot{ID: uuid.New(), Base: "USD", Quote: "ZWG", Rate: r, Source: "seed", CapturedAt: time.Now().UTC().Truncate(time.Second)}
}

func TestCacheSetGetRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	c := cache.New(client, time.Minute)
	ctx := context.Background()

	_, ok := c.Get(ctx, "USD", "ZWG")
	assert.False(t, ok, "cold cache is a miss")

	in := snap()
	c.Set(ctx, in)
	got, ok := c.Get(ctx, "USD", "ZWG")
	require.True(t, ok)
	assert.True(t, got.Rate.Equal(in.Rate))
	assert.Equal(t, "seed", got.Source)
}

func TestCacheInvalidate(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	c := cache.New(client, time.Minute)
	ctx := context.Background()
	c.Set(ctx, snap())
	c.Invalidate(ctx, "USD", "ZWG")
	_, ok := c.Get(ctx, "USD", "ZWG")
	assert.False(t, ok)
}

func TestCacheTTLExpiry(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	c := cache.New(client, time.Minute)
	ctx := context.Background()
	c.Set(ctx, snap())
	mr.FastForward(2 * time.Minute)
	_, ok := c.Get(ctx, "USD", "ZWG")
	assert.False(t, ok, "entry expired after the ttl")
}

// TestCacheGracefulDegradationNilClient proves the service still works when
// redis is not configured: every op is a clean miss, never a panic or error.
func TestCacheGracefulDegradationNilClient(t *testing.T) {
	c := cache.New(nil, time.Minute)
	ctx := context.Background()
	c.Set(ctx, snap()) // no panic
	_, ok := c.Get(ctx, "USD", "ZWG")
	assert.False(t, ok)
	c.Invalidate(ctx, "USD", "ZWG") // no panic
}

// TestCacheGracefulDegradationDeadServer proves a redis outage is a miss, not
// an error: conversion falls back to the database.
func TestCacheGracefulDegradationDeadServer(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	c := cache.New(client, time.Minute)
	ctx := context.Background()
	c.Set(ctx, snap())
	mr.Close() // server gone
	_, ok := c.Get(ctx, "USD", "ZWG")
	assert.False(t, ok, "dead redis is a miss, not an error")
	c.Set(ctx, snap())              // no panic
	c.Invalidate(ctx, "USD", "ZWG") // no panic
}
