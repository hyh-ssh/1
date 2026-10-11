package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// Adapter converts normalized protocol types to/from a specific upstream
// protocol and performs the actual HTTP request.
type Adapter interface {
	// Type returns the provider type this adapter handles.
	Type() string

	// Chat performs a non-streaming chat completion.
	Chat(ctx context.Context, req *protocol.ChatRequest) (*protocol.ChatResponse, error)

	// ChatStream performs a streaming chat completion, emitting events to the
	// provided callback until completion or error.
	ChatStream(ctx context.Context, req *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error

	// Responses performs a non-streaming Responses API call.
	Responses(ctx context.Context, req *protocol.ResponsesRequest) (*protocol.ResponsesResponse, error)

	// ResponsesStream performs a streaming Responses API call.
	ResponsesStream(ctx context.Context, req *protocol.ResponsesRequest, emit func(protocol.StreamEvent) error) error

	// ListModels attempts model discovery. Returns an error if unsupported.
	ListModels(ctx context.Context) ([]string, error)
}

// Config carries the runtime configuration for an adapter instance.
type Config struct {
	BaseURL     string
	APIKey      string
	ExtraHeader map[string]string
	TimeoutMS   int
	HTTPClient  *http.Client
}

// Error is an upstream error with classification.
type Error struct {
	StatusCode int
	Type       string // "connection" | "timeout" | "blocked" | "rate_limit" | "server" | "client" | "auth"
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	return fmt.Sprintf("upstream error status=%d type=%s: %s", e.StatusCode, e.Type, e.Message)
}

// IsRetryable reports whether this error should trigger failover.
func (e *Error) IsRetryable() bool {
	switch e.Type {
	case "connection", "timeout", "rate_limit", "server":
		return true
	default:
		return false
	}
}

// ClassifyHTTPError maps an HTTP status code to an Error type.
func ClassifyHTTPError(status int, body []byte, headers ...http.Header) *Error {
	e := &Error{StatusCode: status, Message: string(body)}
	if len(headers) > 0 {
		e.RetryAfter = ParseRetryAfter(headers[0].Get("Retry-After"), time.Now())
	}
	switch {
	case status == 401 || status == 403:
		e.Type = "auth"
	case status == 429:
		e.Type = "rate_limit"
	case status >= 500:
		e.Type = "server"
	case status >= 400:
		e.Type = "client"
	default:
		e.Type = "unknown"
	}
	return e
}

// ParseRetryAfter accepts seconds or an HTTP date, capped at five minutes.
func ParseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(min(seconds, 300)) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return min(at.Sub(now), 5*time.Minute)
	}
	return 0
}

// IsRetryableHTTP reports whether an HTTP status code should trigger failover.
func IsRetryableHTTP(status int) bool {
	return status == 429 || status >= 500
}

// ErrUnsupported is returned when an adapter does not support an operation.
var ErrUnsupported = errors.New("unsupported operation")

// ErrInvalidRequest marks a request the adapter refuses locally because the
// normalized request cannot be represented safely for the target protocol
// (an inexpressible tool choice, an unassociated tool result, or a payload
// conflict). It only ever wraps adapter-side validation, never an upstream
// HTTP response: upstream failures keep their *Error classification.
var ErrInvalidRequest = errors.New("request cannot be represented for the provider protocol")
