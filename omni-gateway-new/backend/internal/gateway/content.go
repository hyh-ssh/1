package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func (s *Service) persistContent(ctx context.Context, prompt, response any) {
	if s == nil || !s.contentEnabled || s.contentStore == nil || s.contentCipher == nil {
		return
	}
	metadata, ok := requestMetadataFromContext(ctx)
	if !ok || metadata.ID == "" {
		return
	}
	promptBytes, err := json.Marshal(prompt)
	if err != nil {
		slog.Error("request content encoding failed")
		return
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		slog.Error("response content encoding failed")
		return
	}
	truncated := false
	promptBytes, promptTruncated := truncateContent(promptBytes, s.contentMaxBytes)
	responseBytes, responseTruncated := truncateContent(responseBytes, s.contentMaxBytes)
	truncated = promptTruncated || responseTruncated
	promptEncrypted, err := s.contentCipher.Encrypt(string(promptBytes))
	if err != nil {
		slog.Error("request content encryption failed")
		return
	}
	responseEncrypted, err := s.contentCipher.Encrypt(string(responseBytes))
	if err != nil {
		slog.Error("response content encryption failed")
		return
	}
	content := &model.ContentLog{RequestID: metadata.ID, Prompt: []byte(promptEncrypted), Response: []byte(responseEncrypted), Truncated: truncated, CreatedAt: time.Now().UTC()}
	if err := s.writer.Submit(func(ctx context.Context) error { return s.contentStore.InsertContentLog(ctx, content) }); err != nil {
		slog.Error("content persistence rejected", "request_id", metadata.ID)
	}
}

func truncateContent(value []byte, maxBytes int) ([]byte, bool) {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value, false
	}
	return append([]byte(nil), value[:maxBytes]...), true
}

// Keep at most one byte beyond the limit so persistence marks truncation,
// without retaining an entire long-lived stream in memory.
func captureStreamContent(current string, event any, maxBytes int) string {
	if len(current) > maxBytes {
		return current
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return current
	}
	remaining := maxBytes + 1 - len(current)
	if len(raw)+1 > remaining {
		return current + string(raw[:min(len(raw), remaining)])
	}
	return current + string(raw) + "\n"
}
