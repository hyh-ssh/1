package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// probeRecordingStore captures what the probe path persists, so the regression
// test can assert the reason kept for the provider row.
type probeRecordingStore struct {
	*adminTestStore
	verifications []probeVerification
}

type probeVerification struct {
	id      int64
	healthy bool
	reason  string
}

func (s *probeRecordingStore) RecordProviderVerification(_ context.Context, id int64, healthy bool, reason string) error {
	s.verifications = append(s.verifications, probeVerification{id: id, healthy: healthy, reason: reason})
	if provider := s.providers[id]; provider != nil {
		if healthy {
			provider.LastError = ""
		} else {
			provider.LastError = reason
		}
	}
	return nil
}

func newProbeHarness(t *testing.T, base *adminTestStore) (http.Handler, *http.Cookie, string, *probeRecordingStore) {
	t.Helper()
	store := &probeRecordingStore{adminTestStore: base}
	// An empty AuthConfig keeps the default upstream client, which applies the
	// same network policy as production.
	handler := NewHandlerWithGatewayAndCipher(nil, store, store, nil, AuthConfig{}, nil)
	cookie, csrf := adminTestSession(t, handler)
	return handler, cookie, csrf, store
}

func TestAdminProviderVerifyExplainsBlockedUpstream(t *testing.T) {
	base := newAdminTestStore()
	// Loopback is refused by the network policy before any dial happens.
	provider := &model.Provider{ID: 1, Name: "blocked", Type: model.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:9/v1", Active: true, TimeoutMS: 1000}
	base.providers[provider.ID] = provider
	handler, cookie, csrf, store := newProbeHarness(t, base)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/providers/1/verify", strings.NewReader(""))
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", csrf)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("verify status = %d, want %d (body %s)", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode verify error: %v (body %s)", err, recorder.Body.String())
	}
	if body.Error.Code != "upstream_blocked" {
		t.Fatalf("error code = %q, want upstream_blocked (body %s)", body.Error.Code, recorder.Body.String())
	}
	if !strings.Contains(body.Error.Message, "blocked by the network policy") {
		t.Fatalf("error message = %q, want the policy reason", body.Error.Message)
	}
	if len(store.verifications) != 1 {
		t.Fatalf("probe records = %d, want 1", len(store.verifications))
	}
	recorded := store.verifications[0]
	if recorded.healthy || recorded.id != provider.ID || recorded.reason != body.Error.Message {
		t.Fatalf("probe record = %+v, want a failed probe carrying %q", recorded, body.Error.Message)
	}
	if provider.LastError != body.Error.Message {
		t.Fatalf("provider last_error = %q, want %q", provider.LastError, body.Error.Message)
	}
}
