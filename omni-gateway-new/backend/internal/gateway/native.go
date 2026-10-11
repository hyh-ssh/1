package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// Native shares admission, ordered routing, health and settlement with Chat.
func (s *Service) Native(ctx context.Context, req *protocol.NativeRequest) (*protocol.NativeResponse, error) {
	return s.runNative(ctx, req, nil)
}

// NativeStream never changes upstream after any SSE frame has been delivered.
func (s *Service) NativeStream(ctx context.Context, req *protocol.NativeRequest, emit func(protocol.NativeEvent) error) error {
	if emit == nil {
		return errors.New("nil native stream callback")
	}
	_, err := s.runNative(ctx, req, emit)
	return err
}

func (s *Service) runNative(ctx context.Context, req *protocol.NativeRequest, emit func(protocol.NativeEvent) error) (*protocol.NativeResponse, error) {
	if req == nil {
		return nil, errors.New("nil native request")
	}
	started := time.Now()
	recorder := newRequestRecorder(s, ctx)
	finish := func(err error, usage model.Usage) {
		if recorder != nil {
			status := model.StatusSuccess
			code := http.StatusOK
			if err != nil {
				status = model.StatusError
				code = 0
			}
			recorder.finish(status, code, err, usage, started)
		}
	}
	release, err := s.acquirePolicy(ctx, req.Model)
	if err != nil {
		finish(err, model.NewUsage())
		return nil, err
	}
	defer release()
	routes, err := s.resolveRoutes(ctx, req.Model)
	if err != nil {
		finish(err, model.NewUsage())
		return nil, err
	}
	// Cross-protocol Messages conversion can decline a route without making an
	// upstream call. Keep searching for a native Anthropic passthrough route.
	conversionRejected := false
	lastErr := adapter.ErrUnsupported
	for _, route := range routes {
		native, ok := route.adapter.(adapter.NativeAdapter)
		if !ok {
			continue
		}
		releaseRoute, err := s.acquireRoute(ctx, route)
		if err != nil {
			lastErr = err
			continue
		}
		attemptStarted := time.Now()
		attempt := recorder.attempt(route, attemptStarted)
		upstreamReq := *req
		upstreamReq.Model = route.providerModel
		usage := model.NewUsage()
		emitted := false
		firstContent := false
		var response *protocol.NativeResponse
		var callErr error
		var content []byte
		outputBytes := 0
		if emit == nil {
			response, callErr = native.Native(ctx, &upstreamReq)
			if callErr == nil && response == nil {
				callErr = errors.New("empty native upstream response")
			}
			if response != nil {
				usage = response.Usage
				outputBytes = jsonTextBytes(response.Raw)
			}
			if callErr == nil {
				response.Raw, callErr = protocol.RewriteNativeModel(response.Raw, req.Model, false)
			}
		} else {
			callErr = native.NativeStream(ctx, &upstreamReq, func(event protocol.NativeEvent) error {
				usage = usage.Add(event.Usage)
				if event.Content {
					outputBytes += nativeEventTextBytes(event)
				}
				if event.Failed {
					return &adapter.Error{StatusCode: http.StatusBadGateway, Type: "server", Message: "upstream stream failed"}
				}
				if event.Content && !firstContent {
					firstContent = true
					attempt.TTFTMS = nonNegativeDurationMS(time.Since(attemptStarted))
				}
				event, err := protocol.RewriteNativeEventModel(event, req.Model)
				if err != nil {
					return err
				}
				if s.contentEnabled && len(content) <= s.contentMaxBytes {
					remaining := s.contentMaxBytes + 1 - len(content)
					content = append(content, event.Wire[:min(len(event.Wire), remaining)]...)
				}
				if len(event.Wire) > 0 {
					emitted = true
				}
				return emit(event)
			})
		}
		releaseRoute()
		if errors.Is(callErr, adapter.ErrUnsupported) && !emitted {
			conversionRejected = conversionRejected || errors.Is(callErr, adapter.ErrInvalidRequest)
			lastErr = callErr
			continue
		}
		if callErr == nil {
			s.markProviderHealthy(ctx, route.providerID)
		} else {
			s.markProviderFailure(ctx, route.providerID, callErr)
		}
		if req.API != protocol.ImagesAPI {
			inputBytes := jsonTextBytes(req.Raw)
			if !hasReportedUsage(usage) && callErr == nil {
				usage = estimateUsage(inputBytes, outputBytes)
			} else if callErr != nil {
				usage = failedAttemptUsage(usage, inputBytes, outputBytes)
			}
		}
		s.settleAttempt(ctx, recorder, attempt, route, usage, callErr == nil)
		status := "success"
		if callErr != nil {
			status = "error"
		}
		finishAttempt(attempt, status, 0, callErr, usage, attemptStarted)
		if recorder != nil {
			recorder.addAttempt(attempt)
		}
		if callErr == nil {
			if emit == nil {
				s.persistContent(ctx, req.Raw, response.Raw)
			} else {
				s.persistContent(ctx, req.Raw, string(content))
			}
			finish(nil, usage)
			return response, nil
		}
		lastErr = callErr
		if emitted || !isRetryable(callErr) {
			s.persistContent(ctx, req.Raw, string(content))
			finish(callErr, usage)
			return nil, callErr
		}
	}
	if req.API == protocol.MessagesAPI && conversionRejected && errors.Is(lastErr, adapter.ErrUnsupported) {
		lastErr = adapter.ErrInvalidRequest
	}
	finish(lastErr, model.NewUsage())
	return nil, lastErr
}

func nativeEventTextBytes(event protocol.NativeEvent) int {
	var lines []string
	for _, line := range strings.Split(string(event.Wire), "\n") {
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(line, "data:"))
		}
	}
	var data struct {
		Delta json.RawMessage `json:"delta"`
	}
	if json.Unmarshal([]byte(strings.Join(lines, "\n")), &data) != nil {
		return 0
	}
	return jsonTextBytes(data.Delta)
}
