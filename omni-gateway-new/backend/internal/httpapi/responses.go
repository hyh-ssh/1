package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type responsesRequestWire struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       string          `json:"instructions"`
	Stream             bool            `json:"stream"`
	PreviousResponseID string          `json:"previous_response_id"`
}

func (h *gatewayHandler) responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	if h.responsesService == nil {
		writeError(w, http.StatusNotImplemented, "server_error", "responses_unsupported", "Responses API is not supported by this gateway")
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "gateway API key required")
		return
	}
	request, err := decodeResponsesRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		return
	}
	if !PrincipalModelAllowed(principal, request.Model) {
		writeError(w, http.StatusForbidden, "permission_error", "model_not_allowed", "the API key is not allowed to use this model")
		return
	}
	if request.Stream {
		h.responsesStream(w, r.WithContext(withGatewayRequestMetadata(r, principal, "responses", request.Model, true)), request)
		return
	}

	response, err := h.responsesService.Responses(withGatewayRequestMetadata(r, principal, "responses", request.Model, false), request)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	if response == nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "empty_response", "upstream returned an empty Responses response")
		return
	}
	encoded, err := makeResponsesResponse(response, request.Model)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "invalid_response", "upstream returned an invalid Responses response")
		return
	}
	writeRawJSON(w, http.StatusOK, encoded)
}

func decodeResponsesRequest(w http.ResponseWriter, r *http.Request) (*protocol.ResponsesRequest, error) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBodyBytes))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON body")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("request body must contain one JSON object")
	}
	if raw == nil {
		return nil, fmt.Errorf("request body must contain a JSON object")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON body")
	}
	var wire responsesRequestWire
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return nil, fmt.Errorf("invalid request fields")
	}
	wire.Model = strings.TrimSpace(wire.Model)
	if wire.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	request := &protocol.ResponsesRequest{
		Model:              wire.Model,
		Input:              copyJSONValue(wire.Input),
		Instructions:       wire.Instructions,
		Stream:             wire.Stream,
		PreviousResponseID: wire.PreviousResponseID,
		Extra:              make(map[string]json.RawMessage),
	}
	known := map[string]bool{
		"model": true, "input": true, "instructions": true, "stream": true, "previous_response_id": true,
	}
	for key, value := range raw {
		if !known[key] {
			request.Extra[key] = copyRaw(value)
		}
	}
	// The Responses API accepts either a bare string or an array of input
	// items. Normalizing here means a malformed input is rejected with a 400
	// instead of being forwarded for the upstream to reject, and the gateway
	// keeps a message view of the request it can orchestrate on.
	messages, err := protocol.NormalizeResponsesInput(request.Input)
	if err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	request.Messages = messages
	return request, nil
}

func makeResponsesResponse(response *protocol.ResponsesResponse, modelName string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if hasJSONValue(response.Raw) {
		if err := json.Unmarshal(response.Raw, &object); err != nil {
			return nil, err
		}
	}
	if object == nil {
		object = make(map[string]json.RawMessage)
	}
	if response.ID != "" {
		object["id"] = mustJSON(response.ID)
	}
	if _, ok := object["object"]; !ok {
		object["object"] = mustJSON("response")
	}
	if modelName != "" {
		object["model"] = mustJSON(modelName)
	}
	if response.Status != "" {
		object["status"] = mustJSON(response.Status)
	}
	if hasJSONValue(response.Output) {
		if _, ok := object["output"]; !ok {
			object["output"] = copyRaw(response.Output)
		}
	} else if _, ok := object["output"]; !ok {
		object["output"] = mustJSON([]any{})
	}
	if _, ok := object["usage"]; !ok && len(response.Usage.Meters) > 0 {
		object["usage"] = responsesUsageJSON(response.Usage)
	}
	return json.Marshal(object)
}

func (h *gatewayHandler) responsesStream(w http.ResponseWriter, r *http.Request, request *protocol.ResponsesRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "server_error", "streaming_unsupported", "streaming is not supported by this server")
		return
	}

	started := false
	doneSent := false
	errorSent := false
	var firstFailure error
	writeEvent := func(event protocol.StreamEvent) error {
		if firstFailure != nil {
			return firstFailure
		}
		if doneSent || errorSent {
			return nil
		}
		// A failed first event is still an HTTP error, not a successful SSE
		// response. Never expose the upstream's error text in the JSON body.
		if event.Type == "error" && !started {
			firstFailure = &adapter.Error{Type: "server", StatusCode: http.StatusBadGateway, Message: "upstream stream failed"}
			return firstFailure
		}
		// normalizeResponsesEvent exposes usage separately when it is embedded in
		// response.completed. The completed raw event already carries that usage,
		// so suppress the intermediate duplicate and send it with the final event.
		if event.Type == "usage" && isResponsesCompletedEvent(event.Raw) {
			return nil
		}

		var frames [][]byte
		switch event.Type {
		case "done":
			doneSent = true
			if hasJSONValue(event.Raw) {
				frames = [][]byte{rewriteResponsesEventModel(event.Raw, request.Model)}
			} else {
				frames = [][]byte{[]byte("[DONE]")}
			}
		case "error":
			errorSent = true
			if hasJSONValue(event.Raw) {
				frames = [][]byte{rewriteResponsesEventModel(event.Raw, request.Model)}
			} else {
				payload := errorBody(r.Context(), "upstream_error", "upstream_error", event.Error)
				payload["type"] = "error"
				frames = [][]byte{mustJSON(payload)}
			}
		case "chunk":
			if hasJSONValue(event.Raw) {
				frames = [][]byte{rewriteResponsesEventModel(event.Raw, request.Model)}
			} else {
				// One event may carry several tool-call fragments, and each one
				// is a separate Responses delta frame.
				frames = makeResponsesDeltas(event)
			}
		case "usage":
			if hasJSONValue(event.Raw) {
				frames = [][]byte{rewriteResponsesEventModel(event.Raw, request.Model)}
			} else {
				frames = [][]byte{mustJSON(map[string]any{
					"type":  "response.usage",
					"usage": json.RawMessage(responsesUsageJSON(event.Usage)),
				})}
			}
		default:
			return nil
		}

		for _, data := range frames {
			if !started {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Set("X-Accel-Buffering", "no")
				w.WriteHeader(http.StatusOK)
				started = true
			}
			name := responsesSSEEventName(data, event.Event)
			if name != "" {
				if _, err := fmt.Fprintf(w, "event: %s\n", name); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return err
			}
			flusher.Flush()
		}
		return nil
	}

	err := h.responsesService.ResponsesStream(r.Context(), request, writeEvent)
	if firstFailure != nil {
		writeGatewayError(w, firstFailure)
		return
	}
	if err != nil && !started {
		writeGatewayError(w, err)
		return
	}
	if err != nil && started && !doneSent && !errorSent {
		_ = writeEvent(protocol.StreamEvent{Type: "error", Error: "upstream stream failed"})
	}
	if err == nil && !doneSent && !errorSent {
		_ = writeEvent(protocol.StreamEvent{Type: "done"})
	}
}

// responsesSSEEventName preserves the upstream Responses event name alongside
// its raw data. Prefer the payload's type when present, falling back to the
// normalized name for providers which only send an SSE event header.
func responsesSSEEventName(data []byte, fallback string) string {
	var payload struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Type != "" {
		fallback = payload.Type
	}
	if (strings.HasPrefix(fallback, "response.") || fallback == "error") && !strings.ContainsAny(fallback, "\r\n") {
		return fallback
	}
	return ""
}

// makeResponsesDeltas renders a normalized event the gateway produced itself
// (rather than proxied from upstream) as one or more Responses delta frames.
// Tool-call argument fragments become response.function_call_arguments.delta so
// a client using the Responses protocol can reassemble the call exactly as the
// Chat path can.
func makeResponsesDeltas(event protocol.StreamEvent) [][]byte {
	if len(event.ToolCalls) > 0 {
		payloads := make([][]byte, 0, len(event.ToolCalls))
		for _, delta := range event.ToolCalls {
			payload := map[string]any{
				"type":         "response.function_call_arguments.delta",
				"output_index": delta.Index,
				"delta":        delta.Arguments,
			}
			if delta.ID != "" {
				payload["item_id"] = delta.ID
			}
			if delta.Name != "" {
				payload["name"] = delta.Name
			}
			payloads = append(payloads, mustJSON(payload))
		}
		return payloads
	}

	text := ""
	for _, part := range event.Delta.Content {
		if part.Type == "text" || part.Type == "output_text" {
			text += part.Text
		}
	}
	payload := map[string]any{
		"type":  "response.output_text.delta",
		"delta": text,
	}
	if event.Index != 0 || event.Event != "" {
		payload["output_index"] = event.Index
	}
	return [][]byte{mustJSON(payload)}
}

func responsesUsageJSON(usage model.Usage) json.RawMessage {
	payload := map[string]any{
		"input_tokens":  usage.Get("input"),
		"output_tokens": usage.Get("output"),
		"total_tokens":  usage.Get("total"),
	}
	if payload["total_tokens"] == int64(0) {
		payload["total_tokens"] = usage.Get("input") + usage.Get("output")
	}
	if cached := usage.Get("cache_input"); cached != 0 {
		payload["input_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if reasoning := usage.Get("reasoning"); reasoning != 0 {
		payload["output_tokens_details"] = map[string]any{"reasoning_tokens": reasoning}
	}
	return mustJSON(payload)
}

func rewriteResponsesEventModel(raw json.RawMessage, modelName string) []byte {
	if !hasJSONValue(raw) {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return copyRaw(raw)
	}
	if modelName != "" {
		if _, ok := object["model"]; ok {
			object["model"] = mustJSON(modelName)
		}
		if responseRaw, ok := object["response"]; ok {
			var responseObject map[string]json.RawMessage
			if json.Unmarshal(responseRaw, &responseObject) == nil {
				if _, hasModel := responseObject["model"]; hasModel {
					responseObject["model"] = mustJSON(modelName)
					if encoded, err := json.Marshal(responseObject); err == nil {
						object["response"] = encoded
					}
				}
			}
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return copyRaw(raw)
	}
	return encoded
}

func isResponsesCompletedEvent(raw json.RawMessage) bool {
	if !hasJSONValue(raw) {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	var eventType string
	if json.Unmarshal(object["type"], &eventType) != nil {
		return false
	}
	return eventType == "response.completed" || eventType == "response.incomplete"
}

func copyJSONValue(value json.RawMessage) json.RawMessage {
	if !hasJSONValue(value) {
		return nil
	}
	return copyRaw(value)
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func writeRawJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
