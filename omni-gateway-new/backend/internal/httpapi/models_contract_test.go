package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// GET /v1/models is a read-only catalog of active aliases filtered by the
// calling key. The contract tests pin authentication, the allow-list
// intersection with group grants, envelope shape and the sanitized failure
// path; upstream model identifiers must never appear.

func TestModelsContractRejectsMissingAndUnknownKeys(t *testing.T) {
	store := &contractRouteStore{aliases: map[string]*model.ModelAlias{
		"chat-alias": {ID: 1, Name: "chat-alias", Active: true},
	}}
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, store, keys, nil)

	for _, tc := range []struct {
		name   string
		header string
	}{
		{"missing key", ""},
		{"unknown key", "Bearer sk-not-issued"},
		{"wrong scheme", "Basic " + secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if tc.header != "" {
				request.Header.Set("Authorization", tc.header)
			}
			handler.ServeHTTP(recorder, request)

			status, errorType, code, _ := decodeErrorEnvelope(t, recorder)
			if status != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d: %s", status, http.StatusUnauthorized, recorder.Body.String())
			}
			if errorType != "authentication_error" || (code != "api_key_required" && code != "invalid_api_key") {
				t.Fatalf("unexpected auth envelope: type=%q code=%q", errorType, code)
			}
			if strings.Contains(recorder.Body.String(), "chat-alias") {
				t.Fatal("unauthenticated response leaked the model catalog")
			}
		})
	}

	for _, tc := range []struct {
		name   string
		update func(*fakeAPIKeyStore)
	}{
		{"inactive key", func(k *fakeAPIKeyStore) { k.key.Active = false }},
		{"inactive user", func(k *fakeAPIKeyStore) { k.user.Active = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inactiveKeys, inactiveSecret := contractAPIKeys()
			tc.update(inactiveKeys)
			inactiveHandler := newContractHandler(t, store, inactiveKeys, nil)
			recorder := gatewayRequest(t, inactiveHandler, http.MethodGet, "/v1/models", inactiveSecret, "")
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}
}

func TestModelsContractListsOnlyAllowedActiveAliases(t *testing.T) {
	store := &contractRouteStore{aliases: map[string]*model.ModelAlias{
		"vision-alias": {ID: 1, Name: "vision-alias", Active: true},
		"chat-alias":   {ID: 2, Name: "chat-alias", Active: true},
		"retired":      {ID: 3, Name: "retired", Active: false},
	}}
	// The upstream provider model name is a local secret from the caller's
	// point of view; it must never be listed.
	store.entries = map[int64][]*model.RouteEntry{1: {
		{ID: 1, AliasID: 1, ProviderID: 9, ProviderModel: "internal-vendor-model", Priority: 1, Active: true},
	}}
	keys, secret := contractAPIKeys("chat-alias")
	handler := newContractHandler(t, store, keys, nil)

	recorder := gatewayRequest(t, handler, http.MethodGet, "/v1/models", secret, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content type = %q, want application/json", contentType)
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	if payload.Object != "list" || len(payload.Data) != 1 || payload.Data[0].ID != "chat-alias" {
		t.Fatalf("unexpected filtered catalog: %#v", payload)
	}
	if payload.Data[0].Object != "model" || payload.Data[0].OwnedBy == "" {
		t.Fatalf("model entry is missing its OpenAI fields: %#v", payload.Data[0])
	}
	body := recorder.Body.String()
	for _, leaked := range []string{"vision-alias", "retired", "internal-vendor-model"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("catalog leaked %q: %s", leaked, body)
		}
	}
}

func TestModelsContractUnrestrictedKeyGetsSortedActiveAliases(t *testing.T) {
	store := &contractRouteStore{aliases: map[string]*model.ModelAlias{
		"zeta":  {ID: 1, Name: "zeta", Active: true},
		"alpha": {ID: 2, Name: "alpha", Active: true},
		"off":   {ID: 3, Name: "off", Active: false},
	}}
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, store, keys, nil)

	recorder := gatewayRequest(t, handler, http.MethodGet, "/v1/models", secret, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	if len(payload.Data) != 2 || payload.Data[0].ID != "alpha" || payload.Data[1].ID != "zeta" {
		t.Fatalf("unexpected catalog ordering/filtering: %#v", payload.Data)
	}
}

func TestModelsContractRetiredGroupGrantsCannotRestrictKeys(t *testing.T) {
	store := &contractRouteStore{aliases: map[string]*model.ModelAlias{
		"chat-alias":   {ID: 1, Name: "chat-alias", Active: true},
		"vision-alias": {ID: 2, Name: "vision-alias", Active: true},
		"embed-alias":  {ID: 3, Name: "embed-alias", Active: true},
	}}
	keys, secret := contractAPIKeys("chat-alias", "vision-alias", "embed-alias")
	grouped := &groupedAPIKeyStore{fakeAPIKeyStore: *keys, access: &model.ModelAccess{Restricted: true, Models: []string{"chat-alias", "vision-alias"}}}
	handler := newContractHandler(t, store, grouped, nil)

	recorder := gatewayRequest(t, handler, http.MethodGet, "/v1/models", secret, "")
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	if len(payload.Data) != 3 || payload.Data[0].ID != "chat-alias" || payload.Data[1].ID != "embed-alias" || payload.Data[2].ID != "vision-alias" {
		t.Fatalf("historical group grants restricted the key: %#v", payload.Data)
	}

	// Empty historical grants do not deny models; the key whitelist still does.
	grouped.access.Models = nil
	grouped.key.ModelAllow = []string{"embed-alias"}
	recorder = gatewayRequest(t, handler, http.MethodGet, "/v1/models", secret, "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"embed-alias"`) || strings.Contains(recorder.Body.String(), "vision-alias") {
		t.Fatalf("key whitelist catalog = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestModelsContractStoreFailureIsSanitized(t *testing.T) {
	store := &contractRouteStore{aliasErr: errors.New("pq: password=secret host=db")}
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, store, keys, nil)

	recorder := gatewayRequest(t, handler, http.MethodGet, "/v1/models", secret, "")
	status, errorType, code, message := decodeErrorEnvelope(t, recorder)
	if status != http.StatusInternalServerError || errorType != "server_error" || code != "models_unavailable" {
		t.Fatalf("unexpected failure envelope: %d %s %s %s", status, errorType, code, message)
	}
	if strings.Contains(recorder.Body.String(), "password") || strings.Contains(recorder.Body.String(), "pq:") {
		t.Fatalf("store error leaked: %s", recorder.Body.String())
	}
}

func TestModelsContractMethodNotAllowed(t *testing.T) {
	store := &contractRouteStore{aliases: map[string]*model.ModelAlias{"chat-alias": {ID: 1, Name: "chat-alias", Active: true}}}
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, store, keys, nil)

	recorder := gatewayRequest(t, handler, http.MethodPost, "/v1/models", secret, `{}`)
	status, errorType, code, _ := decodeErrorEnvelope(t, recorder)
	if status != http.StatusMethodNotAllowed || errorType != "invalid_request_error" || code != "method_not_allowed" {
		t.Fatalf("unexpected method refusal: %d %s %s", status, errorType, code)
	}
}
