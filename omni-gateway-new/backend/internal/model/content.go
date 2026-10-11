package model

import "time"

// ContentLog stores optional encrypted request/response bodies. The gateway
// never writes this record unless content logging is explicitly enabled.
type ContentLog struct {
	RequestID string    `json:"request_id"`
	Prompt    []byte    `json:"-"`
	Response  []byte    `json:"-"`
	Truncated bool      `json:"truncated"`
	CreatedAt time.Time `json:"created_at"`
}
