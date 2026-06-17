// Package cache wraps the Redis client. Week 1 keeps this thin; rate limits
// and pubsub fan-out arrive in week 3.
package cache

import "github.com/redis/go-redis/v9"

type Client struct{ rdb *redis.Client }

func NewClient(rdb *redis.Client) *Client { return &Client{rdb: rdb} }

func (c *Client) Raw() *redis.Client { return c.rdb }
