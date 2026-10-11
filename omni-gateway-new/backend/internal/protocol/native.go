package protocol

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/zjvtit/omni-gateway/internal/model"
)

type NativeAPI string

const (
	MessagesAPI   NativeAPI = "messages"
	EmbeddingsAPI NativeAPI = "embeddings"
	ImagesAPI     NativeAPI = "images_generations"
)

// NativeRequest retains all wire fields. Model and Stream are validated by
// the HTTP boundary; only Model is replaced when dispatching to a route.
type NativeRequest struct {
	API    NativeAPI
	Model  string
	Stream bool
	Raw    json.RawMessage
}

type NativeResponse struct {
	Raw   json.RawMessage
	Usage model.Usage
}

// NativeEvent retains complete SSE frames, including comments and metadata.
// Usage contains increments, never cumulative counters.
type NativeEvent struct {
	Wire    []byte
	Usage   model.Usage
	Content bool
	Done    bool
	Failed  bool
}

// RewriteNativeModel rewrites the root model and a Messages stream's message
// model, leaving arbitrary nested user payloads and unknown JSON fields intact.
func RewriteNativeModel(raw json.RawMessage, name string, require bool) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("expected a JSON object")
	}
	changed := false
	if _, exists := object["model"]; exists || require {
		object["model"], _ = json.Marshal(name)
		changed = true
	}
	var eventType string
	_ = json.Unmarshal(object["type"], &eventType)
	if eventType == "message_start" && len(object["message"]) > 0 {
		message, err := RewriteNativeModel(object["message"], name, false)
		if err != nil {
			return nil, err
		}
		object["message"] = message
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(object)
}

// RewriteNativeEventModel keeps SSE event/id/retry/comment lines in place and
// rewrites only JSON data containing a model field.
func RewriteNativeEventModel(event NativeEvent, name string) (NativeEvent, error) {
	lines := strings.Split(strings.TrimSuffix(string(event.Wire), "\n\n"), "\n")
	var data []string
	for _, line := range lines {
		if line == "data" {
			data = append(data, "")
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if len(data) == 0 || strings.TrimSpace(strings.Join(data, "\n")) == "[DONE]" {
		return event, nil
	}
	rewritten, err := RewriteNativeModel(json.RawMessage(strings.Join(data, "\n")), name, false)
	if err != nil {
		return event, err
	}
	var output []string
	inserted := false
	for _, line := range lines {
		if line == "data" || strings.HasPrefix(line, "data:") {
			if !inserted {
				output = append(output, "data: "+string(rewritten))
				inserted = true
			}
		} else {
			output = append(output, line)
		}
	}
	event.Wire = []byte(strings.Join(output, "\n") + "\n\n")
	return event, nil
}
