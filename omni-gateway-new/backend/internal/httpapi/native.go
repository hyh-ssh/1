package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type NativeService interface {
	Native(context.Context, *protocol.NativeRequest) (*protocol.NativeResponse, error)
	NativeStream(context.Context, *protocol.NativeRequest, func(protocol.NativeEvent) error) error
}

func (h *gatewayHandler) messages(w http.ResponseWriter, r *http.Request) {
	h.native(w, r, protocol.MessagesAPI)
}
func (h *gatewayHandler) embeddings(w http.ResponseWriter, r *http.Request) {
	h.native(w, r, protocol.EmbeddingsAPI)
}
func (h *gatewayHandler) images(w http.ResponseWriter, r *http.Request) {
	h.native(w, r, protocol.ImagesAPI)
}

func decodeNative(w http.ResponseWriter, r *http.Request, api protocol.NativeAPI) (*protocol.NativeRequest, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxChatBodyBytes))
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, errors.New("request must be a JSON object")
	}
	req := &protocol.NativeRequest{API: api, Raw: raw}
	if json.Unmarshal(object["model"], &req.Model) != nil || strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("model is required")
	}
	if value, ok := object["stream"]; ok {
		if string(value) == "null" || json.Unmarshal(value, &req.Stream) != nil {
			return nil, errors.New("stream must be a boolean")
		}
	}
	switch api {
	case protocol.MessagesAPI:
		var messages []json.RawMessage
		var limit int
		if json.Unmarshal(object["messages"], &messages) != nil || len(messages) == 0 || json.Unmarshal(object["max_tokens"], &limit) != nil || limit <= 0 {
			return nil, errors.New("messages and positive max_tokens are required")
		}
	case protocol.EmbeddingsAPI:
		if object["input"] == nil || string(object["input"]) == "null" {
			return nil, errors.New("input is required")
		}
		if req.Stream {
			return nil, errors.New("Embeddings does not support streaming")
		}
	case protocol.ImagesAPI:
		var prompt string
		if json.Unmarshal(object["prompt"], &prompt) != nil || strings.TrimSpace(prompt) == "" {
			return nil, errors.New("prompt is required")
		}
	default:
		return nil, errors.New("unsupported native API")
	}
	return req, nil
}

func (h *gatewayHandler) native(w http.ResponseWriter, r *http.Request, kind protocol.NativeAPI) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, 401, "authentication_error", "unauthorized", "gateway API key required")
		return
	}
	req, err := decodeNative(w, r, kind)
	if err != nil {
		writeError(w, 400, "invalid_request_error", "invalid_request", err.Error())
		return
	}
	if !PrincipalModelAllowed(principal, req.Model) {
		writeError(w, 403, "permission_error", "model_not_allowed", "model is not allowed for this API key")
		return
	}
	service, ok := h.service.(NativeService)
	if !ok {
		writeError(w, 501, "server_error", "unsupported_operation", "native API is not supported")
		return
	}
	ctx := withGatewayRequestMetadata(r, principal, string(kind), req.Model, req.Stream)
	if !req.Stream {
		response, err := service.Native(ctx, req)
		if err != nil {
			writeGatewayError(w, err)
			return
		}
		if response == nil || !json.Valid(response.Raw) {
			writeError(w, 502, "upstream_error", "invalid_response", "upstream returned an invalid response")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(response.Raw)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "server_error", "streaming_unavailable", "streaming unavailable")
		return
	}
	started, failed, failedMessages := false, false, false
	err = service.NativeStream(ctx, req, func(event protocol.NativeEvent) error {
		if failedMessages {
			return errors.New("upstream stream failed")
		}
		// Never forward an upstream error frame verbatim: it can contain
		// provider secrets. Before the first frame we can still send JSON 502;
		// afterwards the handler emits exactly one sanitized SSE error.
		if event.Failed {
			if kind == protocol.MessagesAPI {
				failedMessages = true
				return errors.New("upstream stream failed")
			}
			failed = true
		}
		if len(event.Wire) == 0 {
			return nil
		}
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			started = true
		}
		if _, err := w.Write(event.Wire); err != nil {
			return err
		}
		flusher.Flush()
		return r.Context().Err()
	})
	if failedMessages && err == nil {
		err = errors.New("upstream stream failed")
	}
	if err != nil && !started {
		writeGatewayError(w, err)
		return
	}
	if err != nil && (!failed || kind == protocol.MessagesAPI) && r.Context().Err() == nil {
		payload := errorBody(ctx, "api_error", "stream_interrupted", "upstream stream interrupted")
		payload["type"] = "error"
		if encoded, marshalErr := json.Marshal(payload); marshalErr == nil {
			_, _ = io.WriteString(w, "event: error\ndata: "+string(encoded)+"\n\n")
			flusher.Flush()
		}
	}
}
