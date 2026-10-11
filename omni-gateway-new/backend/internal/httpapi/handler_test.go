package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePinger struct {
	err   error
	calls int
}

func (p *fakePinger) Ping(context.Context) error {
	p.calls++
	return p.err
}

func TestHealthAndRequestID(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(RequestIDHeader, "client-request-1")

	NewHandler(nil).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get(RequestIDHeader); got != "client-request-1" {
		t.Fatalf("got request ID %q, want client-request-1", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("got content type %q", got)
	}
}

func TestRequestIDGeneratedWhenMissingOrInvalid(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("x", requestIDLimit+1), "bad\r\nid"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(RequestIDHeader, value)
		NewHandler(nil).ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("input %q: got status %d", value, recorder.Code)
		}
		requestID := recorder.Header().Get(RequestIDHeader)
		if requestID == "" || len(requestID) > requestIDLimit || strings.ContainsAny(requestID, "\r\n") {
			t.Fatalf("input %q: invalid generated request ID %q", value, requestID)
		}
	}
}

func TestRepeatedClientTraceIDsHaveIndependentRecordIDs(t *testing.T) {
	ids := map[string]bool{}
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) != "same-client-trace" {
			t.Error("client trace ID was not preserved")
		}
		id, _ := r.Context().Value(recordIDContextKey).(string)
		if len(id) != 32 || id != w.Header().Get("X-Gateway-Request-ID") || ids[id] {
			t.Fatalf("invalid or reused record identity: %q", id)
		}
		ids[id] = true
	}))
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set(RequestIDHeader, "same-client-trace")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func TestReadiness(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCalls  int
	}{
		{name: "ready", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "database error", err: errors.New("database down"), wantStatus: http.StatusServiceUnavailable, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinger := &fakePinger{err: tc.err}
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

			NewHandler(pinger).ServeHTTP(recorder, req)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("got status %d, want %d", recorder.Code, tc.wantStatus)
			}
			if pinger.calls != tc.wantCalls {
				t.Fatalf("Ping called %d times, want %d", pinger.calls, tc.wantCalls)
			}
		})
	}
}

func TestReadinessWithoutDatabase(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	NewHandler(nil).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestUnknownPath(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)

	NewHandler(nil).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestEmbeddedFrontendFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	NewHandler(nil).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "omni-gateway") {
		t.Fatalf("unexpected embedded frontend response: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
}
