package session

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const clientElicitationPrefix = "clientelicitation:"

const userTokenFieldPrefix = "token:"

// taskRecordPrefix namespaces A2A ownership records (agent-assigned id -> principal).
const taskRecordPrefix = "taskowner:"

// Cache implements a cache
type Cache struct {
	inmemory      *sync.Map
	innerMu       sync.Mutex // serializes copy-on-write mutations on inner map[string]string values
	extClient     *redis.Client
	encryptionKey []byte
}

// KeyExists checks if a key exists in the cache
func (c *Cache) KeyExists(ctx context.Context, key string) (bool, error) {
	if c.inmemory != nil {
		_, ok := c.inmemory.Load(key)
		return ok, nil
	}
	count, err := c.extClient.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	return false, nil

}

// GetSession returns a session from the cache
func (c *Cache) GetSession(ctx context.Context, key string) (map[string]string, error) {
	if c.inmemory != nil {
		val, ok := c.inmemory.Load(key)
		if ok {
			return val.(map[string]string), nil
		}
		return map[string]string{}, nil
	}
	return c.extClient.HGetAll(ctx, key).Result()
}

// DeleteSessions deletes sessions and associated metadata from the cache
func (c *Cache) DeleteSessions(ctx context.Context, key ...string) error {
	if c.inmemory != nil {
		c.innerMu.Lock()
		defer c.innerMu.Unlock()
		for _, k := range key {
			c.inmemory.Delete(k)
			c.inmemory.Delete(clientElicitationPrefix + k)
		}
		return nil
	}
	allKeys := make([]string, 0, len(key)*2)
	for _, k := range key {
		allKeys = append(allKeys, k, clientElicitationPrefix+k)
	}
	return c.extClient.Del(ctx, allKeys...).Err()
}

// AddSession will add a session under the key. If the key exists it will append that session.
// ttl sets the expiry on the Redis hash key; pass 0 for no expiry (in-memory mode ignores ttl).
func (c *Cache) AddSession(ctx context.Context, key, mcpServerID, mcpSession string, ttl time.Duration) (bool, error) {
	if c.inmemory != nil {
		c.innerMu.Lock()
		defer c.innerMu.Unlock()
		var existing map[string]string
		if val, ok := c.inmemory.Load(key); ok {
			existing = val.(map[string]string)
		}
		next := maps.Clone(existing)
		if next == nil {
			next = map[string]string{}
		}
		next[mcpServerID] = mcpSession
		c.inmemory.Store(key, next)
		return true, nil
	}
	pipe := c.extClient.Pipeline()
	pipe.HSet(ctx, key, mcpServerID, mcpSession)
	if ttl > 0 {
		pipe.Expire(ctx, key, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveServerSession remove specific server session form cache
func (c *Cache) RemoveServerSession(ctx context.Context, key, mcpServerID string) error {
	if c.inmemory != nil {
		c.innerMu.Lock()
		defer c.innerMu.Unlock()
		val, ok := c.inmemory.Load(key)
		if !ok {
			return nil
		}
		existing := val.(map[string]string)
		next := maps.Clone(existing)
		delete(next, mcpServerID)
		c.inmemory.Store(key, next)
		return nil
	}
	return c.extClient.HDel(ctx, key, mcpServerID).Err()
}

// SetClientElicitation records that the client for this gateway session supports elicitation.
// ttl sets the key expiry in Redis; pass 0 for no expiry (in-memory mode ignores ttl).
func (c *Cache) SetClientElicitation(ctx context.Context, gatewaySessionID string, ttl time.Duration) error {
	key := clientElicitationPrefix + gatewaySessionID
	if c.inmemory != nil {
		c.inmemory.Store(key, true)
		return nil
	}
	return c.extClient.Set(ctx, key, "1", ttl).Err()
}

// GetClientElicitation returns whether the client for this gateway session supports elicitation
func (c *Cache) GetClientElicitation(ctx context.Context, gatewaySessionID string) (bool, error) {
	key := clientElicitationPrefix + gatewaySessionID
	if c.inmemory != nil {
		_, ok := c.inmemory.Load(key)
		return ok, nil
	}
	val, err := c.extClient.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return val == "1", nil
}

// SetUserToken stores a per-user upstream token in the session hash.
// ttl sets the expiry on the Redis hash key; pass 0 for no expiry (in-memory mode ignores ttl).
func (c *Cache) SetUserToken(ctx context.Context, sessionID, serverName, token string, ttl time.Duration) error {
	field := userTokenFieldPrefix + serverName
	if c.inmemory != nil {
		c.innerMu.Lock()
		defer c.innerMu.Unlock()
		var existing map[string]string
		if val, ok := c.inmemory.Load(sessionID); ok {
			existing = val.(map[string]string)
		}
		next := maps.Clone(existing)
		if next == nil {
			next = map[string]string{}
		}
		next[field] = token
		c.inmemory.Store(sessionID, next)
		return nil
	}
	value := token
	if c.encryptionKey != nil {
		encrypted, err := encrypt(c.encryptionKey, token)
		if err != nil {
			return fmt.Errorf("encrypting user token: %w", err)
		}
		value = encrypted
	}
	pipe := c.extClient.Pipeline()
	pipe.HSet(ctx, sessionID, field, value)
	if ttl > 0 {
		pipe.Expire(ctx, sessionID, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return nil
}

// GetUserToken retrieves a cached upstream token. Returns ("", false, nil) on miss.
// JWT tokens are checked for expiry; expired tokens are deleted and treated as a miss.
func (c *Cache) GetUserToken(ctx context.Context, sessionID, serverName string) (string, bool, error) {
	field := userTokenFieldPrefix + serverName
	if c.inmemory != nil {
		c.innerMu.Lock()
		defer c.innerMu.Unlock()
		val, ok := c.inmemory.Load(sessionID)
		if !ok {
			return "", false, nil
		}
		m := val.(map[string]string)
		token, ok := m[field]
		if !ok {
			return "", false, nil
		}
		if checkUpstreamJWTExpiry(token) {
			next := maps.Clone(m)
			delete(next, field)
			c.inmemory.Store(sessionID, next)
			return "", false, nil
		}
		return token, true, nil
	}
	raw, err := c.extClient.HGet(ctx, sessionID, field).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	token := raw
	if c.encryptionKey != nil {
		decrypted, err := decrypt(c.encryptionKey, raw)
		if err != nil {
			return "", false, fmt.Errorf("decrypting user token: %w", err)
		}
		token = decrypted
	}
	if checkUpstreamJWTExpiry(token) {
		_ = c.DeleteUserToken(ctx, sessionID, serverName)
		return "", false, nil
	}
	return token, true, nil
}

// DeleteUserToken removes a cached upstream token for the given session and server.
func (c *Cache) DeleteUserToken(ctx context.Context, sessionID, serverName string) error {
	field := userTokenFieldPrefix + serverName
	if c.inmemory != nil {
		c.innerMu.Lock()
		defer c.innerMu.Unlock()
		val, ok := c.inmemory.Load(sessionID)
		if !ok {
			return nil
		}
		m := val.(map[string]string)
		next := maps.Clone(m)
		delete(next, field)
		c.inmemory.Store(sessionID, next)
		return nil
	}
	return c.extClient.HDel(ctx, sessionID, field).Err()
}

// taskRecordKey builds the ownership-record key. agent is a config-derived agent
// name ("{namespace}/{registrationName}") that never contains ':', so it delimits
// the key unambiguously even though id is agent-assigned and may contain any bytes:
// no two distinct (agent, id) pairs can collide.
func taskRecordKey(agent, id string) string {
	return taskRecordPrefix + agent + ":" + id
}

// StoreTaskRecord records ownership of an agent-assigned id (a task or context id)
// by principal, insert-only: the first writer wins and later writes never overwrite.
// It returns the principal that owns the id after the call — the existing owner if
// one was already recorded, otherwise principal — and whether this call created the
// record. principal is an identity claim (the OAuth sub) kept for later ownership
// comparison, not a credential, so it is stored as-is. ttl bounds the record in
// Redis; in-memory mode ignores ttl, matching the other cache methods.
func (c *Cache) StoreTaskRecord(ctx context.Context, agent, id, principal string, ttl time.Duration) (string, bool, error) {
	key := taskRecordKey(agent, id)
	if c.inmemory != nil {
		actual, loaded := c.inmemory.LoadOrStore(key, principal)
		return actual.(string), !loaded, nil
	}
	// SET key principal NX (with TTL): atomic insert-only write.
	_, err := c.extClient.SetArgs(ctx, key, principal, redis.SetArgs{Mode: "NX", TTL: ttl}).Result()
	if err == nil {
		return principal, true, nil
	}
	if !errors.Is(err, redis.Nil) {
		return "", false, err
	}
	// NX failed: the record already existed — report the current owner
	owner, gerr := c.extClient.Get(ctx, key).Result()
	if errors.Is(gerr, redis.Nil) {
		// expired between the two calls: treat as no owner rather than reporting a stale one
		return "", false, nil
	}
	if gerr != nil {
		return "", false, gerr
	}
	return owner, false, nil
}

// LookupTaskRecord returns the principal that owns an agent-assigned id and whether
// a record was found. Returns ("", false, nil) on miss.
func (c *Cache) LookupTaskRecord(ctx context.Context, agent, id string) (string, bool, error) {
	key := taskRecordKey(agent, id)
	if c.inmemory != nil {
		val, ok := c.inmemory.Load(key)
		if !ok {
			return "", false, nil
		}
		return val.(string), true, nil
	}
	owner, err := c.extClient.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return owner, true, nil
}

// DeleteTaskRecord removes the ownership record for an agent-assigned id.
func (c *Cache) DeleteTaskRecord(ctx context.Context, agent, id string) error {
	key := taskRecordKey(agent, id)
	if c.inmemory != nil {
		c.inmemory.Delete(key)
		return nil
	}
	return c.extClient.Del(ctx, key).Err()
}

// NewCache returns a new cache. Pass WithRedisClient to use an external redis
// store; otherwise an in-memory cache is returned.
func NewCache(opts ...func(*Cache)) (*Cache, error) {
	c := &Cache{}
	for _, opt := range opts {
		opt(c)
	}
	if c.extClient != nil {
		return c, nil
	}
	c.inmemory = &sync.Map{}
	return c, nil
}

// WithRedisClient configures the cache to use an existing redis client
func WithRedisClient(client *redis.Client) func(c *Cache) {
	return func(c *Cache) {
		if client != nil {
			c.extClient = client
		}
	}
}

// WithEncryptionKey sets the AES-256 key for encrypting user tokens in Redis.
func WithEncryptionKey(key []byte) func(c *Cache) {
	return func(c *Cache) {
		c.encryptionKey = key
	}
}
