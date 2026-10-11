package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func auditFilterFromQuery(r *http.Request) (model.AuditFilter, error) {
	q := r.URL.Query()
	f := model.AuditFilter{Limit: 50, Resource: strings.TrimSpace(q.Get("resource")), Action: strings.TrimSpace(q.Get("action")), Result: strings.TrimSpace(q.Get("result"))}
	var err error
	if raw := q.Get("actor_id"); raw != "" {
		f.ActorID, err = parseAdminID(raw)
		if err != nil {
			return f, errors.New("invalid actor_id")
		}
	}
	if raw := q.Get("limit"); raw != "" {
		f.Limit, err = strconv.Atoi(raw)
		if err != nil {
			return f, errors.New("invalid limit")
		}
	}
	if raw := q.Get("offset"); raw != "" {
		f.Offset, err = strconv.Atoi(raw)
		if err != nil {
			return f, errors.New("invalid offset")
		}
	}
	if raw := q.Get("from"); raw != "" {
		f.From, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, errors.New("invalid from")
		}
		f.From = f.From.UTC()
	}
	if raw := q.Get("to"); raw != "" {
		f.To, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, errors.New("invalid to")
		}
		f.To = f.To.UTC()
	}
	if f.Limit < 1 || f.Limit > 200 || f.Offset < 0 {
		return f, errors.New("limit must be 1..200 and offset nonnegative")
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return f, errors.New("from must not be after to")
	}
	switch f.Result {
	case "", "unknown", "success", "failure", "denied":
	default:
		return f, errors.New("invalid result")
	}
	return f, nil
}
