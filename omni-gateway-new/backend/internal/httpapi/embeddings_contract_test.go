package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// POST /v1/embeddings is an OpenAI-compatible native passthrough: the gateway
// rewrites the model alias on the way out and back, preserves every native
// field, meters the upstream usage and refuses streaming before any upstream
// call is attempted.

func TestEmbeddingsContractRejectsStreamTrueBeforeUpstream(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	})
	defer upstream.Close()

	store := singleProviderStore(t, "embed-alias", "vendor-embed", upstream.server.URL)
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, store, keys, upstream.client())

	for _, tc := range []struct {
		name string
		body string
	}{
		{"stream true", `{"model":"embed-alias","input":"hello","stream":true}`},
		{"non-boolean stream", `{"model":"embed-alias","input":"hello","stream":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := gatewayRequest(t, handler, http.MethodPost, "/v1/embeddings", secret, tc.body)
			status, errorType, code, _ := decodeErrorEnvelope(t, recorder)
			if status != http.StatusBadRequest || errorType != "invalid_request_error" || code != "invalid_request" {
				t.Fatalf("unexpected refusal: %d %s %s (%s)", status, errorType, code, recorder.Body.String())
			}
			if !strings.Contains(strings.ToLower(recorder.Body.String()), "stream") {
				t.Fatalf("refusal did not name the stream field: %s", recorder.Body.String())
			}
		})
	}
	if upstream.callCount() != 0 {
		t.Fatalf("embeddings stream request reached the upstream %d time(s)", upstream.callCount())
	}

	// stream=false is the documented shape and must stay accepted.
	upstream.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	}
	recorder := gatewayRequest(t, handler, http.MethodPost, "/v1/embeddings", secret, `{"model":"embed-alias","input":"hello","stream":false}`)
	if recorder.Code != http.StatusOK || upstream.callCount() != 1 {
		t.Fatalf("stream=false was not forwarded: %d %s (calls=%d)", recorder.Code, recorder.Body.String(), upstream.callCount())
	}
}

func TestEmbeddingsContractForwardsNativeFieldsAndRewritesAlias(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"vendor-embed","usage":{"prompt_tokens":8,"total_tokens":8}}`)
	})
	defer upstream.Close()

	store := singleProviderStore(t, "embed-alias", "vendor-embed", upstream.server.URL)
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, store, keys, upstream.client())

	body := `{"model":"embed-alias","input":["alpha","beta"],"encoding_format":"base64","dimensions":256,"user":"user-7","provider_extension":{"keep":true}}`
	recorder := gatewayRequest(t, handler, http.MethodPost, "/v1/embeddings", secret, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	call := upstream.lastCall(t)
	if call.Method != http.MethodPost || call.Path != "/embeddings" {
		t.Fatalf("upstream call = %s %s, want POST /embeddings", call.Method, call.Path)
	}
	if call.Authorization != "Bearer "+contractUpstreamSecret {
		t.Fatalf("upstream credential = %q, want the provider secret", call.Authorization)
	}
	if call.ContentType != "application/json" {
		t.Fatalf("upstream content type = %q", call.ContentType)
	}
	var forwarded map[string]json.RawMessage
	if err := json.Unmarshal(call.Body, &forwarded); err != nil {
		t.Fatalf("decode forwarded body: %v (%s)", err, call.Body)
	}
	for field, want := range map[string]string{
		"model":              `"vendor-embed"`,
		"input":              `["alpha","beta"]`,
		"encoding_format":    `"base64"`,
		"dimensions":         `256`,
		"user":               `"user-7"`,
		"provider_extension": `{"keep":true}`,
	} {
		if string(forwarded[field]) != want {
			t.Fatalf("forwarded %s = %s, want %s (body=%s)", field, forwarded[field], want, call.Body)
		}
	}

	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("client content type = %q, want application/json", contentType)
	}
	responseBody := recorder.Body.String()
	if !strings.Contains(responseBody, `"model":"embed-alias"`) {
		t.Fatalf("response model was not rewritten to the alias: %s", responseBody)
	}
	if strings.Contains(responseBody, "vendor-embed") {
		t.Fatalf("response leaked the upstream model name: %s", responseBody)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode embeddings response: %v", err)
	}
	if string(response["data"]) != `[{"object":"embedding","embedding":[0.1,0.2],"index":0}]` {
		t.Fatalf("native data field changed: %s", response["data"])
	}
	if string(response["usage"]) != `{"prompt_tokens":8,"total_tokens":8}` {
		t.Fatalf("native usage field changed: %s", response["usage"])
	}
}

func TestEmbeddingsContractMetersNativeUsageUnderAlias(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[],"model":"vendor-embed","usage":{"prompt_tokens":8,"total_tokens":8}}`)
	})
	defer upstream.Close()

	store := singleProviderStore(t, "embed-alias", "vendor-embed", upstream.server.URL)
	keys, secret := contractAPIKeys()
	service := newContractService(t, store, upstream.client())
	settlement := &contractSettlement{}
	service.SetSettlement(settlement)
	handler := NewHandlerWithGateway(nil, nil, keys, service, AuthConfig{})

	recorder := gatewayRequest(t, handler, http.MethodPost, "/v1/embeddings", secret, `{"model":"embed-alias","input":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	usage, alias, success := settlement.last()
	if !success || alias != "embed-alias" {
		t.Fatalf("settlement = success=%v alias=%q", success, alias)
	}
	if usage.Get("input") != 8 || usage.Get("total") != 8 {
		t.Fatalf("metered usage = %v, want input/total 8", usage.Meters)
	}
}

func TestEmbeddingsContractMapsUpstreamFailuresWithoutLeakingBody(t *testing.T) {
	for _, tc := range []struct {
		name       string
		upstream   func(http.ResponseWriter, *http.Request)
		wantStatus int
		wantType   string
		wantCode   string
	}{
		{"upstream 400", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream-secret-detail","type":"invalid_request_error"}}`)
		}, http.StatusBadGateway, "upstream_error", "upstream_error"},
		{"upstream 401", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream-secret-detail"}}`)
		}, http.StatusBadGateway, "upstream_error", "upstream_authentication_failed"},
		{"upstream 429", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream-secret-detail"}}`)
		}, http.StatusTooManyRequests, "rate_limit_error", "upstream_rate_limit"},
		{"upstream 500", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream-secret-detail"}}`)
		}, http.StatusBadGateway, "upstream_error", "upstream_error"},
		{"non-object success body", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[1,2,3]`)
		}, http.StatusBadGateway, "upstream_error", "upstream_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newContractUpstream(tc.upstream)
			defer upstream.Close()
			store := singleProviderStore(t, "embed-alias", "vendor-embed", upstream.server.URL)
			keys, secret := contractAPIKeys()
			handler := newContractHandler(t, store, keys, upstream.client())

			recorder := gatewayRequest(t, handler, http.MethodPost, "/v1/embeddings", secret, `{"model":"embed-alias","input":"hello"}`)
			status, errorType, code, _ := decodeErrorEnvelope(t, recorder)
			if status != tc.wantStatus || errorType != tc.wantType || code != tc.wantCode {
				t.Fatalf("envelope = %d %s %s, want %d %s %s (%s)", status, errorType, code, tc.wantStatus, tc.wantType, tc.wantCode, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "upstream-secret-detail") {
				t.Fatalf("upstream error body leaked: %s", recorder.Body.String())
			}
			if upstream.callCount() != 1 {
				t.Fatalf("upstream calls = %d, want 1", upstream.callCount())
			}
		})
	}
}

func TestEmbeddingsContractPermissionAndRequestValidation(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	})
	defer upstream.Close()

	newHandlerFor := func(t *testing.T, modelAllow ...string) (http.Handler, string) {
		keys, secret := contractAPIKeys(modelAllow...)
		return newContractHandler(t, singleProviderStore(t, "embed-alias", "vendor-embed", upstream.server.URL), keys, upstream.client()), secret
	}

	allowListed, allowSecret := newHandlerFor(t)
	denied, deniedSecret := newHandlerFor(t, "other-alias")

	for _, tc := range []struct {
		name       string
		handler    http.Handler
		secret     string
		method     string
		body       string
		wantStatus int
		wantType   string
		wantCode   string
	}{
		{"model not allowed", denied, deniedSecret, http.MethodPost, `{"model":"embed-alias","input":"hi"}`, http.StatusForbidden, "permission_error", "model_not_allowed"},
		{"unknown alias", allowListed, allowSecret, http.MethodPost, `{"model":"missing-alias","input":"hi"}`, http.StatusNotFound, "invalid_request_error", "model_not_found"},
		{"missing input", allowListed, allowSecret, http.MethodPost, `{"model":"embed-alias"}`, http.StatusBadRequest, "invalid_request_error", "invalid_request"},
		{"null input", allowListed, allowSecret, http.MethodPost, `{"model":"embed-alias","input":null}`, http.StatusBadRequest, "invalid_request_error", "invalid_request"},
		{"missing model", allowListed, allowSecret, http.MethodPost, `{"input":"hi"}`, http.StatusBadRequest, "invalid_request_error", "invalid_request"},
		{"malformed JSON", allowListed, allowSecret, http.MethodPost, `{"model":`, http.StatusBadRequest, "invalid_request_error", "invalid_request"},
		{"method not allowed", allowListed, allowSecret, http.MethodGet, "", http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := gatewayRequest(t, tc.handler, tc.method, "/v1/embeddings", tc.secret, tc.body)
			status, errorType, code, _ := decodeErrorEnvelope(t, recorder)
			if status != tc.wantStatus || errorType != tc.wantType || code != tc.wantCode {
				t.Fatalf("envelope = %d %s %s, want %d %s %s (%s)", status, errorType, code, tc.wantStatus, tc.wantType, tc.wantCode, recorder.Body.String())
			}
		})
	}

	if upstream.callCount() != 0 {
		t.Fatalf("rejected embeddings requests reached the upstream %d time(s)", upstream.callCount())
	}
}
