package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCache_TaskRecord_InMemory(t *testing.T) {
	cache, err := NewCache()
	require.NoError(t, err)
	runTaskRecordSuite(t, cache)
}

func TestCache_TaskRecord_Redis(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache, err := NewCache(WithRedisClient(client))
	require.NoError(t, err)
	runTaskRecordSuite(t, cache)
}

// runTaskRecordSuite exercises the ownership-store contract against either backend.
func runTaskRecordSuite(t *testing.T, cache *Cache) {
	ctx := context.Background()
	const agent = "mcp-test/weather"

	t.Run("store new then lookup", func(t *testing.T) {
		owner, stored, err := cache.StoreTaskRecord(ctx, agent, "task-new", "alice", time.Hour)
		require.NoError(t, err)
		require.True(t, stored)
		require.Equal(t, "alice", owner)

		got, found, err := cache.LookupTaskRecord(ctx, agent, "task-new")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "alice", got)
	})

	t.Run("insert-only: first writer wins", func(t *testing.T) {
		_, stored, err := cache.StoreTaskRecord(ctx, agent, "task-io", "alice", time.Hour)
		require.NoError(t, err)
		require.True(t, stored)

		owner, stored, err := cache.StoreTaskRecord(ctx, agent, "task-io", "bob", time.Hour)
		require.NoError(t, err)
		require.False(t, stored, "a second write must not create a record")
		require.Equal(t, "alice", owner, "the first writer must remain the owner")

		got, _, err := cache.LookupTaskRecord(ctx, agent, "task-io")
		require.NoError(t, err)
		require.Equal(t, "alice", got)
	})

	t.Run("lookup miss", func(t *testing.T) {
		got, found, err := cache.LookupTaskRecord(ctx, agent, "nope")
		require.NoError(t, err)
		require.False(t, found)
		require.Empty(t, got)
	})

	t.Run("delete then lookup misses", func(t *testing.T) {
		_, _, err := cache.StoreTaskRecord(ctx, agent, "task-del", "alice", time.Hour)
		require.NoError(t, err)
		require.NoError(t, cache.DeleteTaskRecord(ctx, agent, "task-del"))
		_, found, err := cache.LookupTaskRecord(ctx, agent, "task-del")
		require.NoError(t, err)
		require.False(t, found)
	})

	t.Run("delete missing is a no-op", func(t *testing.T) {
		require.NoError(t, cache.DeleteTaskRecord(ctx, agent, "never"))
	})

	t.Run("isolation across agents and colon-bearing ids", func(t *testing.T) {
		// same id under two agents must be two independent records
		_, _, err := cache.StoreTaskRecord(ctx, "mcp-test/weather", "a2a-task-1", "alice", time.Hour)
		require.NoError(t, err)
		_, _, err = cache.StoreTaskRecord(ctx, "mcp-test/forecast", "a2a-task-1", "bob", time.Hour)
		require.NoError(t, err)
		// an agent-assigned id containing a colon is still a distinct record — the
		// colon-free agent name keeps the key unambiguous
		_, _, err = cache.StoreTaskRecord(ctx, "mcp-test/weather", "ctx:99", "carol", time.Hour)
		require.NoError(t, err)

		w, _, _ := cache.LookupTaskRecord(ctx, "mcp-test/weather", "a2a-task-1")
		f, _, _ := cache.LookupTaskRecord(ctx, "mcp-test/forecast", "a2a-task-1")
		c, _, _ := cache.LookupTaskRecord(ctx, "mcp-test/weather", "ctx:99")
		require.Equal(t, "alice", w)
		require.Equal(t, "bob", f)
		require.Equal(t, "carol", c)
	})
}

func TestCache_TaskRecord_RedisTTL(t *testing.T) {
	ctx := context.Background()
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache, err := NewCache(WithRedisClient(client))
	require.NoError(t, err)

	_, stored, err := cache.StoreTaskRecord(ctx, "mcp-test/weather", "task-ttl", "alice", time.Hour)
	require.NoError(t, err)
	require.True(t, stored)

	ttl, err := client.TTL(ctx, taskRecordKey("mcp-test/weather", "task-ttl")).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)

	redisServer.FastForward(time.Hour + time.Second)
	_, found, err := cache.LookupTaskRecord(ctx, "mcp-test/weather", "task-ttl")
	require.NoError(t, err)
	require.False(t, found, "the record must expire after its ttl")
}
