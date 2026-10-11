package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// This exercises the real router -> gateway -> adapter -> upstream path. A
// direct HTTP service double cannot detect a gateway that swallows a terminal
// response.failed event before it reaches the Responses writer.
func TestResponsesContractPreservesFailedEventAfterOutput(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"model\":\"vendor-model\"}}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp-1\",\"model\":\"vendor-model\",\"status\":\"failed\"},\"error\":{\"code\":\"upstream_failed\"}}\n\n")
		w.(http.Flusher).Flush()
	})
	defer upstream.Close()
	store := singleProviderStore(t, "public-model", "vendor-model", upstream.server.URL)
	store.providers[1].SupportsResponses = true
	keys, secret := contractAPIKeys()
	service := newContractService(t, store, upstream.client())
	settlement := &contractSettlement{}
	service.SetSettlement(settlement)
	handler := NewHandlerWithGateway(nil, nil, keys, service, AuthConfig{})
	response := gatewayRequest(t, handler, http.MethodPost, "/v1/responses", secret, `{"model":"public-model","input":"hello","stream":true}`)
	if response.Code != 200 || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	frames := parseResponsesSSEFrames(t, response.Body.String())
	if len(frames) != 2 || frames[0].name != "response.created" || frames[1].name != "response.failed" ||
		!strings.Contains(string(frames[0].data["response"]), `"model":"public-model"`) ||
		!strings.Contains(string(frames[1].data["response"]), `"model":"public-model"`) ||
		!strings.Contains(string(frames[1].data["error"]), `"code":"upstream_failed"`) ||
		strings.Contains(response.Body.String(), "response.completed") || strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("failed Responses stream was altered or completed: %s", response.Body.String())
	}
	_, alias, success := settlement.last()
	if alias != "public-model" || success {
		t.Fatalf("failed stream settlement = alias=%q success=%v", alias, success)
	}
}
