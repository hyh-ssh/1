package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
)

const (
	RequestIDHeader = "X-Request-ID"
	requestIDLimit  = 128
)

type contextKey string

const requestIDContextKey contextKey = "llm-gateway-request-id"
const recordIDContextKey contextKey = "llm-gateway-record-id"

// Pinger is the subset of the database pool needed by the readiness endpoint.
type Pinger interface {
	Ping(context.Context) error
}

// NewHandler creates the initial HTTP surface for the gateway. The liveness
// endpoint does not touch the database; readiness verifies the configured
// database connection.
func NewHandler(db Pinger) http.Handler {
	return NewHandlerWithAuth(db, nil, AuthConfig{})
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/assets/") {
		frontend(w, r)
		return
	}
	notFound(w)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readyz(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "not_ready",
				"error":  "database is not configured",
			})
			return
		}
		if err := db.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "not_ready",
				"error":  "database is unavailable",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := validRequestID(r.Header.Get(RequestIDHeader))
		if requestID == "" {
			requestID = newRequestID()
		}
		w.Header().Set(RequestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey, requestID)
		// Caller trace IDs may repeat. A server-owned identity prevents separate
		// calls from colliding in requests, attempts and content logs.
		recordID := newRequestID()
		w.Header().Set("X-Gateway-Request-ID", recordID)
		ctx = context.WithValue(ctx, recordIDContextKey, recordID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > requestIDLimit {
		return ""
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return ""
		}
	}
	return value
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	// rand.Read is expected to be available on supported systems. Returning a
	// stable non-empty fallback still guarantees that every response carries a
	// request ID if the system entropy source is temporarily unavailable.
	return "unavailable"
}

// RequestID returns the request ID assigned by the middleware, if present.
func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "not_found", "not found")
}

type errorDetail struct {
	Message   string `json:"message"`
	Type      string `json:"type"`
	Code      string `json:"code,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// errorBody builds the shared error envelope used by JSON responses and by
// in-stream error events, so a client sees the same shape (message, type, code
// and, when known, the request id) whether the failure happens before or after
// the stream starts.
func errorBody(ctx context.Context, errorType, code, message string) map[string]any {
	requestID := ""
	if ctx != nil {
		requestID, _ = ctx.Value(recordIDContextKey).(string)
	}
	return map[string]any{"error": errorDetail{Message: message, Type: errorType, Code: code, RequestID: requestID}}
}

func writeError(w http.ResponseWriter, status int, errorType, code, message string) {
	// writeError has no request context, so it reads the request id that
	// requestIDMiddleware stored on the response header. The signature stays
	// unchanged so no call site has to pass it through.
	writeJSON(w, status, struct {
		Error errorDetail `json:"error"`
	}{Error: errorDetail{Message: message, Type: errorType, Code: code, RequestID: w.Header().Get("X-Gateway-Request-ID")}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	if v := reflect.ValueOf(value); v.IsValid() && v.Kind() == reflect.Slice && v.IsNil() {
		value = []any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// There is no useful recovery path once headers have been written. The
	// standard library encoder only fails for unsupported values, while all
	// response values in this package are JSON-safe maps.
	_ = json.NewEncoder(w).Encode(value)
}
