// Package storage wraps the MinIO client with the helpers we need for spider
// source distribution and task artifacts. Real upload/download paths arrive
// in week 2; for now we only need a constructor and a presign helper.
package storage

import (
	"context"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
)

type MinIOClient struct {
	mc     *minio.Client
	bucket string
}

func NewMinIOClient(mc *minio.Client, bucket string) *MinIOClient {
	return &MinIOClient{mc: mc, bucket: bucket}
}

// Bucket returns the configured bucket name (used by URL builders).
func (c *MinIOClient) Bucket() string { return c.bucket }

// PresignGet returns a time-limited presigned GET URL for `key`.
func (c *MinIOClient) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := c.mc.PresignedGetObject(ctx, c.bucket, key, ttl, url.Values{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// PresignPut returns a time-limited presigned PUT URL for `key`. Used by the
// Python SDK to upload artifacts directly.
func (c *MinIOClient) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := c.mc.PresignedPutObject(ctx, c.bucket, key, ttl)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
