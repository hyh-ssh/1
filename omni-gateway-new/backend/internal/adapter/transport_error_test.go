package adapter

import (
	"context"
	"errors"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/upstream"
)

// The guarded transport refuses private, loopback and reserved destinations
// before dialing. Model discovery must keep that policy reason visible rather
// than flattening it into a generic connection error.
func TestListModelsClassifiesBlockedUpstream(t *testing.T) {
	guarded := upstream.NewClient(false)
	openaiAdapter := NewOpenAIAdapter(Config{BaseURL: "http://127.0.0.1:9/v1", APIKey: "secret", TimeoutMS: 1000, HTTPClient: guarded})

	_, err := openaiAdapter.ListModels(context.Background())

	var upstreamErr *Error
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("ListModels error = %v, want *Error", err)
	}
	if upstreamErr.Type != "blocked" {
		t.Fatalf("error type = %q, want blocked (%v)", upstreamErr.Type, err)
	}
	if upstreamErr.Message != upstream.ErrBlocked.Error() {
		t.Fatalf("error message = %q, want %q", upstreamErr.Message, upstream.ErrBlocked.Error())
	}
}

// A public destination is not policy-blocked, so the guarded transport reports a
// transport failure instead of a policy failure.
func TestListModelsKeepsConnectionClassForPublicAddress(t *testing.T) {
	guarded := upstream.NewClient(false)
	openaiAdapter := NewOpenAIAdapter(Config{BaseURL: "http://8.8.8.8:9/v1", APIKey: "secret", TimeoutMS: 1000, HTTPClient: guarded})

	_, err := openaiAdapter.ListModels(context.Background())

	var upstreamErr *Error
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("ListModels error = %v, want *Error", err)
	}
	if upstreamErr.Type != "connection" && upstreamErr.Type != "timeout" {
		t.Fatalf("error type = %q, want connection or timeout (%v)", upstreamErr.Type, err)
	}
}
