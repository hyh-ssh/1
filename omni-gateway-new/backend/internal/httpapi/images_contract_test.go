package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/zjvtit/omni-gateway/internal/model"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const imageContractPath = "/v1/images/generations"

func imageContractRequest(ctx context.Context, url, secret, body string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+imageContractPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestImagesContractJSONPreservesFieldsAliasAndMetering(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":42,"model":"vendor-image","data":[{"b64_json":"encoded","revised_prompt":"a sunset"}],"usage":{"input_tokens":3,"output_tokens":4}}`)
	})
	defer upstream.Close()
	store := singleProviderStore(t, "image-alias", "vendor-image", upstream.server.URL)
	keys, secret := contractAPIKeys("image-alias")
	service := newContractService(t, store, upstream.client())
	settlement := &contractSettlement{}
	service.SetSettlement(settlement)
	handler := NewHandlerWithGateway(nil, nil, keys, service, AuthConfig{})

	body := `{"model":"image-alias","prompt":"sunset","size":"1024x1024","quality":"hd","response_format":"b64_json","n":1,"provider_extension":{"keep":true}}`
	resp := gatewayRequest(t, handler, http.MethodPost, imageContractPath, secret, body)
	if resp.Code != 200 || !strings.HasPrefix(resp.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("response = %d %q: %s", resp.Code, resp.Header().Get("Content-Type"), resp.Body.String())
	}
	call := upstream.lastCall(t)
	if call.Method != http.MethodPost || call.Path != "/images/generations" || call.Authorization != "Bearer "+contractUpstreamSecret {
		t.Fatalf("unexpected upstream request: %+v", call)
	}
	var forwarded map[string]json.RawMessage
	if err := json.Unmarshal(call.Body, &forwarded); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"model": `"vendor-image"`, "prompt": `"sunset"`, "size": `"1024x1024"`,
		"quality": `"hd"`, "response_format": `"b64_json"`, "n": `1`,
		"provider_extension": `{"keep":true}`,
	} {
		if string(forwarded[key]) != want {
			t.Fatalf("forwarded %s = %s; want %s", key, forwarded[key], want)
		}
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if string(result["model"]) != `"image-alias"` || string(result["data"]) != `[{"b64_json":"encoded","revised_prompt":"a sunset"}]` {
		t.Fatalf("altered native image response: %s", resp.Body.String())
	}
	usage, alias, success := settlement.last()
	if !success || alias != "image-alias" || usage.Get("image") != 1 {
		t.Fatalf("settlement = %s %v %v", alias, success, usage.Meters)
	}
}

func TestImagesContractAuthAndValidationBeforeUpstream(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unexpected call", 500)
	})
	defer upstream.Close()
	store := singleProviderStore(t, "image-alias", "vendor-image", upstream.server.URL)
	keys, secret := contractAPIKeys("other-alias")
	handler := newContractHandler(t, store, keys, upstream.client())
	for _, tc := range []struct {
		name, key, body string
		want            int
	}{
		{"unauthorized", "invalid-key", `{"model":"image-alias","prompt":"x"}`, 401},
		{"not allowed", secret, `{"model":"image-alias","prompt":"x"}`, 403},
		{"missing prompt", secret, `{"model":"image-alias"}`, 400},
		{"invalid stream", secret, `{"model":"image-alias","prompt":"x","stream":1}`, 400},
		{"invalid JSON", secret, `{"model":`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := gatewayRequest(t, handler, http.MethodPost, imageContractPath, tc.key, tc.body)
			if resp.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", resp.Code, tc.want, resp.Body.String())
			}
		})
	}
	if upstream.callCount() != 0 {
		t.Fatalf("rejected request reached upstream %d times", upstream.callCount())
	}
}

func TestImagesContractSSEFlushesPartialBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	upstream := newContractUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": upstream heartbeat\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"model\":\"vendor-image\",\"partial_image_b64\":\"part\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"model\":\"vendor-image\",\"data\":[{\"b64_json\":\"final\"}]}\n\n")
		w.(http.Flusher).Flush()
	})
	defer upstream.Close()
	keys, secret := contractAPIKeys()
	gateway := httptest.NewServer(newContractHandler(t, singleProviderStore(t, "image-alias", "vendor-image", upstream.server.URL), keys, upstream.client()))
	defer gateway.Close()
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(imageContractRequest(context.Background(), gateway.URL, secret, `{"model":"image-alias","prompt":"x","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d type=%s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() { line, _ := reader.ReadString('\n'); first <- line }()
	select {
	case line := <-first:
		if line != "event: image_generation.partial_image\n" {
			t.Fatalf("first flushed event = %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("partial image buffered until completion")
	}
	close(release)
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, `"type":"image_generation.partial_image"`) != 1 || strings.Count(text, "event: image_generation.completed") != 1 || !strings.Contains(text, `"model":"image-alias"`) || !strings.Contains(text, `"b64_json":"final"`) || strings.Contains(text, "[DONE]") {
		t.Fatalf("image events not preserved: %s", text)
	}
	call := upstream.lastCall(t)
	if call.Accept != "text/event-stream" || !strings.Contains(string(call.Body), `"model":"vendor-image"`) {
		t.Fatalf("upstream stream request = %+v", call)
	}
}

func TestImagesContractStreamErrorsAndNonSSEAreJSONBeforeFirstFrame(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"upstream HTTP error", "application/json", `{"error":{"message":"private-upstream-detail"}}`, 503},
		{"first malformed event", "text/event-stream", "data: {broken}\n\n", 200},
		{"non-SSE success is not invented progress", "application/json", `{"data":[{"b64_json":"final"}]}`, 200},
		{"DONE without completed image", "text/event-stream", "data: [DONE]\n\n", 200},
		{"comment before malformed event", "text/event-stream", ": upstream heartbeat\n\ndata: {broken}\n\n", 200},
		{"empty error event before data", "text/event-stream", ": upstream heartbeat\n\nevent: error\n\n", 200},
		{"Messages terminal is not image completion", "text/event-stream", "data: {\"type\":\"message_stop\"}\n\n", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			defer upstream.Close()
			keys, secret := contractAPIKeys()
			handler := newContractHandler(t, singleProviderStore(t, "image-alias", "vendor-image", upstream.server.URL), keys, upstream.client())
			resp := gatewayRequest(t, handler, http.MethodPost, imageContractPath, secret, `{"model":"image-alias","prompt":"x","stream":true}`)
			if resp.Code != 502 || !json.Valid(resp.Body.Bytes()) || strings.Contains(resp.Body.String(), "private-upstream-detail") || strings.Contains(resp.Body.String(), "[DONE]") {
				t.Fatalf("first-frame error must be JSON 502: %d %s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestImagesContractMidstreamErrorIsSingleSanitizedSSE(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"partial_image_b64\":\"part\"}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"private-upstream-detail\"}}\n\n")
		w.(http.Flusher).Flush()
	})
	defer upstream.Close()
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, singleProviderStore(t, "image-alias", "vendor-image", upstream.server.URL), keys, upstream.client())
	resp := gatewayRequest(t, handler, http.MethodPost, imageContractPath, secret, `{"model":"image-alias","prompt":"x","stream":true}`)
	text := resp.Body.String()
	if resp.Code != 200 || strings.Count(text, "event: error") != 1 || !strings.Contains(text, "partial_image_b64") || strings.Contains(text, "private-upstream-detail") || strings.Contains(text, "image_generation.completed") || strings.Contains(text, "[DONE]") {
		t.Fatalf("stream error termination = %d %s", resp.Code, text)
	}
}

func TestImagesContractEmptyErrorAfterPartialFrame(t *testing.T) {
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"partial_image_b64\":\"part\"}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: error\n\n")
		w.(http.Flusher).Flush()
	})
	defer upstream.Close()
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, singleProviderStore(t, "image-alias", "vendor-image", upstream.server.URL), keys, upstream.client())
	resp := gatewayRequest(t, handler, http.MethodPost, imageContractPath, secret, `{"model":"image-alias","prompt":"x","stream":true}`)
	body := resp.Body.String()
	if resp.Code != 200 || strings.Count(body, "event: error\n") != 1 || !strings.Contains(body, "partial_image_b64") || strings.Contains(body, "image_generation.completed") {
		t.Fatalf("empty error after partial image = %d %s", resp.Code, body)
	}
}

func TestImagesContractDoesNotFailOverAfterPartialFrame(t *testing.T) {
	first := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"partial_image_b64\":\"part\"}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: {broken}\n\n")
		w.(http.Flusher).Flush()
	})
	defer first.Close()
	second := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\"}\n\n")
	})
	defer second.Close()
	store := singleProviderStore(t, "image-alias", "first-model", first.server.URL)
	backup := contractProvider(t, second.server.URL)
	backup.ID = 2
	store.providers[2] = backup
	store.entries[1] = append(store.entries[1], &model.RouteEntry{ID: 2, AliasID: 1, ProviderID: 2, ProviderModel: "second-model", Priority: 20, Active: true})
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, store, keys, first.client())
	resp := gatewayRequest(t, handler, http.MethodPost, imageContractPath, secret, `{"model":"image-alias","prompt":"x","stream":true}`)
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), "partial_image_b64") || strings.Count(resp.Body.String(), "event: error") != 1 || strings.Contains(resp.Body.String(), "image_generation.completed") || second.callCount() != 0 {
		t.Fatalf("unexpected failover after output: status=%d body=%s second calls=%d", resp.Code, resp.Body.String(), second.callCount())
	}
}

func TestImagesContractCancelStopsUpstream(t *testing.T) {
	canceled := make(chan struct{}, 1)
	upstream := newContractUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"partial_image_b64\":\"part\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		canceled <- struct{}{}
	})
	defer upstream.Close()
	keys, secret := contractAPIKeys()
	gateway := httptest.NewServer(newContractHandler(t, singleProviderStore(t, "image-alias", "vendor-image", upstream.server.URL), keys, upstream.client()))
	defer gateway.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(imageContractRequest(ctx, gateway.URL, secret, `{"model":"image-alias","prompt":"x","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "event: image_generation.partial_image\n" {
		t.Fatalf("first event = %q, %v", line, err)
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not reach image upstream")
	}
}
