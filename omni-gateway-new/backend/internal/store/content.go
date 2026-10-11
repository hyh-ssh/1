package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// InsertContentLog stores already-encrypted request/response bodies.
func (s *Store) InsertContentLog(ctx context.Context, log *model.ContentLog) error {
	if log == nil || log.RequestID == "" {
		return errors.New("content request id is required")
	}
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO content_log(request_id, prompt, response, truncated, created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT (request_id) DO NOTHING`, log.RequestID, log.Prompt, log.Response, log.Truncated, log.CreatedAt)
	return err
}

// GetContentLog returns encrypted content for an administrator-controlled
// decryption path. The HTTP layer must never serialize Prompt or Response.
func (s *Store) GetContentLog(ctx context.Context, requestID string) (*model.ContentLog, error) {
	log := &model.ContentLog{}
	err := s.pool.QueryRow(ctx, `SELECT request_id, prompt, response, truncated, created_at FROM content_log WHERE request_id=$1`, requestID).Scan(&log.RequestID, &log.Prompt, &log.Response, &log.Truncated, &log.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return log, err
}
