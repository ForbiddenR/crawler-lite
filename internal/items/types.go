// Package items defines the domain types for spider output. Kept tiny — the
// payload is opaque JSON, so the only contract is the envelope.
package items

import (
	"encoding/json"
	"time"
)

type Item struct {
	ID        int64           `json:"id"`
	TaskID    int64           `json:"task_id"`
	SpiderID  int64           `json:"spider_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type Insert struct {
	TaskID      int64
	SpiderID    int64
	PayloadJSON []byte
}
