// Package artifacts defines the domain types for task output artifacts —
// screenshots, HAR captures, and log indexes. Each is small; payload bytes
// live in MinIO, this package only carries metadata.
package artifacts

import "time"

type Screenshot struct {
	ID         int64     `json:"id"`
	TaskID     int64     `json:"task_id"`
	TakenAt    time.Time `json:"taken_at"`
	Name       string    `json:"name"`
	URL        string    `json:"url,omitempty"`
	StorageKey string    `json:"storage_key"`
	Width      int       `json:"width,omitempty"`
	Height     int       `json:"height,omitempty"`
	Bytes      int       `json:"bytes"`
}

type ScreenshotInsert struct {
	TaskID     int64
	Name       string
	URL        string
	StorageKey string
	Width      int
	Height     int
	Bytes      int
}

type HARInsert struct {
	TaskID       int64
	StorageKey   string
	RequestCount int
	TotalBytes   int64
}

type LogIndex struct {
	TaskID          int64     `json:"task_id"`
	LogKey          string    `json:"log_key"`
	Bytes           int64     `json:"bytes"`
	LineCount       int       `json:"line_count"`
	LevelCountsJSON []byte    `json:"-"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// LogIndexUpsert is a delta — call it as new lines arrive and we ADD bytes
// and line_count, MERGE level_counts (Postgres `||` jsonb operator).
type LogIndexUpsert struct {
	TaskID          int64
	LogKey          string
	AddBytes        int64
	AddLines        int
	LevelCountsJSON []byte
}
