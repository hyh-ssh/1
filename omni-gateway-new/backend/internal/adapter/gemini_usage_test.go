package adapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func TestGeminiStreamEmitsOnlyIncrementalUsage(t *testing.T) {
	stream := "data: " + `{"candidates":[{"content":{"parts":[{"text":"a","thought":true}]} }],"usageMetadata":{"promptTokenCount":3,"totalTokenCount":3,"cachedContentTokenCount":1}}` + "\n\n" +
		"data: " + `{"candidates":[{"content":{"parts":[{"text":"b"}]} }],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4,"thoughtsTokenCount":1,"cachedContentTokenCount":1}}` + "\n\n" +
		"data: " + `{"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4,"thoughtsTokenCount":1,"cachedContentTokenCount":1}}` + "\n\n" +
		"data: " + `{"candidates":[{"content":{"parts":[{"text":"c"}]}}]}` + "\n\n" +
		"data: " + `{"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5,"thoughtsTokenCount":2,"cachedContentTokenCount":1}}` + "\n\n" +
		"data: " + `{"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5,"thoughtsTokenCount":2,"cachedContentTokenCount":2}}` + "\n\n" +
		"data: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()

	adapter := NewGeminiAdapter(Config{BaseURL: server.URL, HTTPClient: server.Client()})
	usage := model.NewUsage()
	usageFrames, doneFrames := 0, 0
	err := adapter.ChatStream(context.Background(), &protocol.ChatRequest{Model: "gemini-pro"}, func(event protocol.StreamEvent) error {
		switch event.Type {
		case "usage":
			usageFrames++
			for meter, value := range event.Usage.Meters {
				if value <= 0 {
					t.Errorf("non-positive %s usage increment: %d", meter, value)
				}
			}
			usage = usage.Add(event.Usage)
		case "done":
			doneFrames++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if usageFrames != 4 || doneFrames != 1 || usage.Get("input") != 3 || usage.Get("output") != 2 || usage.Get("total") != 5 || usage.Get("reasoning") != 2 || usage.Get("cache_input") != 2 {
		t.Fatalf("incremental Gemini usage mismatch: frames=%d done=%d meters=%v", usageFrames, doneFrames, usage.Meters)
	}
}
