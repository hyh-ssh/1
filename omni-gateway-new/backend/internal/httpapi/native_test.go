package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type nativeTestService struct {
	fakeChatService
	calls  int
	events []protocol.NativeEvent
	err    error
}

func (s *nativeTestService) Native(_ context.Context, r *protocol.NativeRequest) (*protocol.NativeResponse, error) {
	s.calls++
	return &protocol.NativeResponse{Raw: json.RawMessage(`{"model":"alias","data":[]}`)}, s.err
}
func (s *nativeTestService) NativeStream(_ context.Context, r *protocol.NativeRequest, emit func(protocol.NativeEvent) error) error {
	s.calls++
	for _, e := range s.events {
		if err := emit(e); err != nil {
			return err
		}
	}
	return s.err
}

func TestNativeRoutesProtectedAndValidated(t *testing.T) {
	for _, tt := range []struct{ path, body string }{{"/v1/messages", `{"model":"alias","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`}, {"/v1/embeddings", `{"model":"alias","input":"hi"}`}, {"/v1/images/generations", `{"model":"alias","prompt":"cat"}`}} {
		t.Run(tt.path, func(t *testing.T) {
			keys := gatewayAPIKeyStore()
			service := &nativeTestService{}
			h := NewHandlerWithGateway(nil, nil, keys, service, AuthConfig{})
			for _, status := range []int{401, 200, 403} {
				r := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
				if status != 401 {
					r.Header.Set("Authorization", "Bearer sk-gateway")
				}
				if status == 403 {
					keys.key.ModelAllow = []string{"denied"}
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != status {
					t.Fatalf("got %d want %d: %s", w.Code, status, w.Body.String())
				}
			}
			if service.calls != 1 {
				t.Fatal("unauthorized call reached service")
			}
		})
	}
	for _, body := range []string{`null`, `{"model":"m","input":"x","stream":true}`, `{"model":"m","input":"x","stream":null}`, `{"model":"m"}`, `{} {}`} {
		r := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
		if _, err := decodeNative(httptest.NewRecorder(), r, protocol.EmbeddingsAPI); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestNativeStreamFailureEndsWithoutFalseSuccess(t *testing.T) {
	s := &nativeTestService{events: []protocol.NativeEvent{{Wire: []byte(": ping\n\n")}}, err: errors.New("upstream secret")}
	h := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), s, AuthConfig{})
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alias","messages":[{}],"max_tokens":10,"stream":true}`))
	r.Header.Set("x-api-key", "sk-gateway")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "event: error") || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("bad SSE %d %s", w.Code, w.Body.String())
	}
}

func TestNativeStreamErrorEnvelopeCarriesCodeAndRequestID(t *testing.T) {
	s := &nativeTestService{events: []protocol.NativeEvent{{Wire: []byte(": ping\n\n")}}, err: errors.New("upstream secret")}
	h := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), s, AuthConfig{})
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alias","messages":[{}],"max_tokens":10,"stream":true}`))
	r.Header.Set("x-api-key", "sk-gateway")
	r.Header.Set(RequestIDHeader, "req-native-error")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := w.Body.String()
	for _, want := range []string{
		`"type":"api_error"`,
		`"code":"stream_interrupted"`,
		`"request_id":"` + w.Header().Get("X-Gateway-Request-ID") + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("native stream error envelope missing %s: %s", want, body)
		}
	}
}

func TestMessagesStreamFirstFrameFailureIsJSONAndAfterFrameIsOneSanitizedSSEError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		events     []protocol.NativeEvent
		wantStatus int
	}{
		{"first failed frame", []protocol.NativeEvent{{Wire: []byte("event: error\ndata: {\"error\":\"upstream secret\"}\n\n"), Failed: true}}, 502},
		{"after message_start", []protocol.NativeEvent{{Wire: []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")}, {Wire: []byte("event: error\ndata: {\"error\":\"upstream secret\"}\n\n"), Failed: true}}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &nativeTestService{events: tc.events}
			h := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), s, AuthConfig{})
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"stream":true}`))
			r.Header.Set("x-api-key", "sk-gateway")
			r.Header.Set(RequestIDHeader, "req-native-stream")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			body := w.Body.String()
			if w.Code != tc.wantStatus || strings.Contains(body, "upstream secret") || strings.Contains(body, "message_stop") {
				t.Fatalf("code=%d body=%s", w.Code, body)
			}
			if tc.wantStatus == 502 {
				if strings.Contains(body, "event: error") || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
					t.Fatalf("first error must be JSON: %s", body)
				}
			} else if strings.Count(body, "event: error\n") != 1 || strings.Count(body, "event: message_start\n") != 1 || !strings.Contains(body, `"request_id":"`+w.Header().Get("X-Gateway-Request-ID")+`"`) || !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
				t.Fatalf("after frame must end with one sanitized SSE error: %s", body)
			}
		})
	}
}
