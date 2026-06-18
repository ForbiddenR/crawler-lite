// Package storage wraps the MinIO client with the helpers we need for spider
// source distribution and task artifacts.
package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// EnsureBucket creates the bucket if it doesn't exist. Called once at startup
// so dev environments don't need to remember to bootstrap MinIO.
func (c *MinIOClient) EnsureBucket(ctx context.Context) error {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("bucket exists: %w", err)
	}
	if exists {
		return nil
	}
	return c.mc.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{})
}

// Upload writes `data` to MinIO under `key`.
func (c *MinIOClient) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key,
		bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType},
	)
	return err
}

// Download fetches an object's bytes. Used by the worker to pull spider source
// zips on each task. For artifacts the UI prefers presigned URLs, but the
// worker is server-side so a plain download is fine.
func (c *MinIOClient) Download(ctx context.Context, key string) ([]byte, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

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

// AppendJSONL appends `lines` (each entry is one JSON object, no newlines
// inside) to a JSONL object at `key`. MinIO doesn't natively support append,
// so we read-modify-write. Acceptable for the log volumes we expect at v1
// scale (a few MB per task); revisit when logs get big.
func (c *MinIOClient) AppendJSONL(ctx context.Context, key string, lines [][]byte) error {
	existing := c.tryDownload(ctx, key)
	var buf bytes.Buffer
	buf.Write(existing)
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	return c.Upload(ctx, key, buf.Bytes(), "application/x-ndjson")
}

// tryDownload returns the bytes of `key` if it exists, or nil if it doesn't.
// Other errors (network, auth) are swallowed — the caller will hit the same
// error on the subsequent Upload and surface it from there.
func (c *MinIOClient) tryDownload(ctx context.Context, key string) []byte {
	if _, err := c.mc.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{}); err != nil {
		return nil
	}
	b, err := c.Download(ctx, key)
	if err != nil {
		return nil
	}
	return b
}
