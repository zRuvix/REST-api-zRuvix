// Package redis wraps a go-redis client and reproduces the command surface of
// zRuvix.Connectivity.Redis, including the zruvix:global_sync pub/sub bridge.
package redis

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// NodeID identifies this process in cross-node global_sync messages. It mirrors
// :erlang.phash2(node()) — a stable identifier unique to this instance.
var NodeID = rand.Int()

// GlobalSyncHandler is invoked when a global_sync message arrives from another
// node. It is wired up at startup (by main) to presence.Sync, which keeps the
// redis package free of a dependency on the presence package.
var GlobalSyncHandler func(userID string, diff map[string]any)

var (
	client *goredis.Client
	ctx    = context.Background()
)

// Connect dials Redis using the provided URI and starts the global_sync
// subscriber. It returns an error if the URI cannot be parsed.
func Connect(uri string) error {
	opts, err := goredis.ParseURL(uri)
	if err != nil {
		return err
	}
	// Bound worst-case latency: if Redis is unreachable, fail fast rather than
	// hanging HTTP handlers (history/stats/kv all touch Redis).
	opts.DialTimeout = 3 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second
	opts.PoolTimeout = 2 * time.Second
	opts.MaxRetries = -1 // no automatic retries/backoff

	client = goredis.NewClient(opts)
	go subscribeGlobalSync()
	return nil
}

func subscribeGlobalSync() {
	sub := client.Subscribe(ctx, "zruvix:global_sync")
	log.Println("Redis: subscribed to zruvix:global_sync")
	for msg := range sub.Channel() {
		var payload struct {
			NodeID int            `json:"node_id"`
			UserID string         `json:"user_id"`
			Diff   map[string]any `json:"diff"`
		}
		if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
			log.Printf("Redis: unknown payload format: %s", msg.Payload)
			continue
		}
		// Ignore messages emitted by this same node.
		if payload.NodeID == NodeID {
			continue
		}
		if GlobalSyncHandler != nil {
			GlobalSyncHandler(payload.UserID, payload.Diff)
		}
	}
}

// Get returns the string value at key, or "" if the key is missing.
func Get(key string) string {
	v, err := client.Get(ctx, key).Result()
	if err != nil {
		return ""
	}
	return v
}

// Set stores value at key.
func Set(key, value string) {
	client.Set(ctx, key, value, 0)
}

// SetEX stores value at key with a TTL. A zero or negative ttl stores without expiry.
func SetEX(key, value string, ttl time.Duration) {
	if ttl <= 0 {
		client.Set(ctx, key, value, 0)
		return
	}
	client.Set(ctx, key, value, ttl)
}

// Del removes key.
func Del(key string) {
	client.Del(ctx, key)
}

// HGetAll returns the full hash at key as a map (empty if missing).
func HGetAll(key string) map[string]string {
	v, err := client.HGetAll(ctx, key).Result()
	if err != nil || v == nil {
		return map[string]string{}
	}
	return v
}

// HGet returns a single hash field value, or "" if missing.
func HGet(key, field string) string {
	v, err := client.HGet(ctx, key, field).Result()
	if err != nil {
		return ""
	}
	return v
}

// HSet sets one field/value pair in the hash at key.
func HSet(key, field, value string) {
	client.HSet(ctx, key, field, value)
}

// HSetMap merges the given map into the hash at key.
func HSetMap(key string, values map[string]string) {
	if len(values) == 0 {
		return
	}
	args := make([]any, 0, len(values)*2)
	for k, v := range values {
		args = append(args, k, v)
	}
	client.HSet(ctx, key, args...)
}

// HDel removes a field from the hash at key.
func HDel(key, field string) {
	client.HDel(ctx, key, field)
}

// HIncrBy atomically increments a hash field by amount.
func HIncrBy(key, field string, amount int64) {
	client.HIncrBy(ctx, key, field, amount)
}

// Publish sends a message to a pub/sub channel.
func Publish(channel, message string) {
	client.Publish(ctx, channel, message)
}

// Script is a Lua script executed server-side, giving callers a compare-and-set
// primitive the individual commands above cannot express. Scripts are declared
// once (at package init) and run via EVALSHA, falling back to EVAL on a cache
// miss — go-redis handles that handshake.
type Script struct{ s *goredis.Script }

// NewScript prepares a Lua script. It does not touch the connection, so it is
// safe to call from a package-level var before Connect runs.
func NewScript(src string) *Script { return &Script{s: goredis.NewScript(src)} }

// RunInt executes the script and returns its integer reply. Any error (Redis
// unreachable, script failure, non-integer reply) yields 0, so callers that use
// a script as a guard fail closed rather than acting on an unverified result.
func (s *Script) RunInt(keys []string, args ...any) int64 {
	v, err := s.s.Run(ctx, client, keys, args...).Int64()
	if err != nil {
		return 0
	}
	return v
}

// LPush prepends a value to the list at key.
func LPush(key, value string) {
	client.LPush(ctx, key, value)
}

// LTrim trims the list at key to the inclusive range [start, stop].
func LTrim(key string, start, stop int64) {
	client.LTrim(ctx, key, start, stop)
}

// LRange returns elements of the list at key in the inclusive range.
func LRange(key string, start, stop int64) []string {
	v, err := client.LRange(ctx, key, start, stop).Result()
	if err != nil {
		return nil
	}
	return v
}

// Incr atomically increments the integer at key and returns the new value.
func Incr(key string) int64 {
	v, err := client.Incr(ctx, key).Result()
	if err != nil {
		return 0
	}
	return v
}

// Expire sets a TTL (in seconds) on key.
func Expire(key string, seconds int) {
	client.Expire(ctx, key, time.Duration(seconds)*time.Second)
}
