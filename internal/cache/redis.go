// Package cache wraps the Redis client. Used for live-log pubsub fanout in
// week 2; rate-limit token buckets land in week 3.
package cache

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

type Client struct{ rdb *redis.Client }

func NewClient(rdb *redis.Client) *Client { return &Client{rdb: rdb} }

// Raw returns the underlying go-redis client. Callers that need PubSub
// or scripting reach through here; we don't try to wrap every primitive.
func (c *Client) Raw() *redis.Client { return c.rdb }

// LogChannel is the Redis pubsub channel name for a task's live log.
// Centralizing the format here lets the LogSink (publisher) and the WS
// handler (subscriber) agree without copy-pasting the format string.
func LogChannel(taskID int64) string {
	return fmt.Sprintf("tasks:%d:log", taskID)
}

// Publish publishes `payload` to the given channel. Wrapped here so callers
// don't need to import go-redis directly.
func (c *Client) Publish(ctx context.Context, channel string, payload []byte) error {
	return c.rdb.Publish(ctx, channel, payload).Err()
}

// Subscribe subscribes to `channel`. Caller must Close() when done.
func (c *Client) Subscribe(ctx context.Context, channel string) *redis.PubSub {
	return c.rdb.Subscribe(ctx, channel)
}
