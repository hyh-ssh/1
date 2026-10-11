package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/background"
	"github.com/zjvtit/omni-gateway/internal/billing"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/policy"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// RecordStore is the persistence surface used for request and upstream attempt
// observability. Writes are bounded and retried without changing the API result.
type RecordStore interface {
	InsertRequest(context.Context, *model.Request) error
	InsertAttempt(context.Context, *model.Attempt) error
}

// RequestPolicy performs preflight admission checks such as budgets and rate limits.
// A nil release function is valid when no concurrency slot was acquired.
type RequestPolicy interface {
	Acquire(context.Context, int64, int64, string) (release func(), err error)
}

// Settlement calculates and persists immutable cost/credits ledger rows for an attempt.
type Settlement interface {
	Settle(context.Context, string, int64, int64, string, string, int64, model.Usage, bool) (*billing.Result, error)
}

// ChargePolicy receives completed settlement totals so admission checks can
// account for charges before the asynchronous ledger write is visible.
type ChargePolicy interface {
	RecordCharge(context.Context, int64, model.Money, model.Money)
}

type settlementChargePolicy interface {
	RecordPendingSettlement(context.Context, int64, string, model.Money, model.Money)
	RecordSettlementFailure(context.Context, int64)
}

// Close drains request/content writes. Call after the HTTP server has stopped
// accepting requests and before closing the store.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.writer.Close(ctx)
}
func (s *Service) PersistenceStats() background.Stats {
	if s == nil {
		return background.Stats{}
	}
	return s.writer.Stats()
}

// ContentStore persists encrypted request and response bodies when explicitly enabled.
type ContentStore interface {
	InsertContentLog(context.Context, *model.ContentLog) error
}

// UsagePolicy receives actual token usage for TPM accounting.
type UsagePolicy interface {
	ObserveUsage(context.Context, int64, int64, string, model.Usage)
}

// RequestMetadata identifies the public request being processed. HTTP adapters
// attach it to the context before invoking the gateway service.
type RequestMetadata struct {
	ID         string
	UserID     int64
	APIKeyID   int64
	API        string
	Streaming  bool
	ModelAlias string
	ClientIP   string
	UserAgent  string
}

type requestMetadataKey struct{}

// WithRequestMetadata annotates a context with request information for
// best-effort Request/Attempt persistence.
func WithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataKey{}, metadata)
}

func requestMetadataFromContext(ctx context.Context) (RequestMetadata, bool) {
	metadata, ok := ctx.Value(requestMetadataKey{}).(RequestMetadata)
	return metadata, ok && strings.TrimSpace(metadata.ID) != ""
}

func (s *Service) acquirePolicy(ctx context.Context, modelAlias string) (func(), error) {
	if s == nil || s.policy == nil {
		return func() {}, nil
	}
	metadata, ok := requestMetadataFromContext(ctx)
	if !ok {
		return func() {}, nil
	}
	release, err := s.policy.Acquire(ctx, metadata.UserID, metadata.APIKeyID, modelAlias)
	if release == nil && err == nil {
		release = func() {}
	}
	return release, err
}

type requestRecorder struct {
	service           *Service
	metadata          RequestMetadata
	started           time.Time
	request           *model.Request
	attempts          []*model.Attempt
	upstreamLatencyMS int64
	ttftMS            int64
	cost              model.Money
	credits           model.Money
	ttftSet           bool
}

func newRequestRecorder(service *Service, ctx context.Context) *requestRecorder {
	metadata, ok := requestMetadataFromContext(ctx)
	if !ok || service == nil {
		return nil
	}
	return &requestRecorder{
		service:  service,
		metadata: metadata,
		started:  time.Now(),
		cost:     model.ZeroMoney,
		credits:  model.ZeroMoney,
	}
}

func (r *requestRecorder) attempt(route resolvedRoute, started time.Time) *model.Attempt {
	return &model.Attempt{
		SettlementID:  billing.NewSettlementID(),
		ProviderID:    route.providerID,
		ProviderModel: route.providerModel,
		Priority:      route.priority,
		CreatedAt:     started.UTC(),
	}
}

func (r *requestRecorder) addAttempt(attempt *model.Attempt) {
	if r == nil || attempt == nil {
		return
	}
	r.attempts = append(r.attempts, attempt)
	r.upstreamLatencyMS += attempt.LatencyMS
	r.cost = r.cost.Add(attempt.Cost)
	ttft := nonNegativeDurationMS(attempt.CreatedAt.Sub(r.started)) + attempt.TTFTMS
	if attempt.TTFTMS > 0 && !r.ttftSet {
		r.ttftMS = ttft
		r.ttftSet = true
	}
	if attempt.TTFTMS == 0 && !r.ttftSet && r.metadata.Streaming && attempt.Status == "success" {
		r.ttftMS = nonNegativeDurationMS(attempt.CreatedAt.Sub(r.started)) + attempt.LatencyMS
		r.ttftSet = true
	}

}

func (r *requestRecorder) finish(status model.RequestStatus, statusCode int, err error, usage model.Usage, totalStarted time.Time) {
	if r == nil {
		return
	}
	if statusCode == 0 {
		if status == model.StatusSuccess {
			statusCode = http.StatusOK
		} else {
			statusCode = http.StatusBadGateway
		}
	}
	errorType, errorSummary, classifiedStatus := recordError(err)
	if status == model.StatusSuccess {
		errorType, errorSummary = "", ""
	} else if classifiedStatus != 0 && statusCode == http.StatusBadGateway {
		statusCode = classifiedStatus
	}
	totalLatency := time.Since(totalStarted).Milliseconds()
	if totalLatency < 0 {
		totalLatency = 0
	}
	r.request = &model.Request{
		ID:                r.metadata.ID,
		UserID:            r.metadata.UserID,
		APIKeyID:          r.metadata.APIKeyID,
		API:               r.metadata.API,
		Streaming:         r.metadata.Streaming,
		ModelAlias:        r.metadata.ModelAlias,
		Status:            status,
		StatusCode:        statusCode,
		ErrorType:         errorType,
		ErrorSummary:      errorSummary,
		Usage:             usage,
		Cost:              r.cost,
		Credits:           r.credits,
		TotalLatencyMS:    totalLatency,
		UpstreamLatencyMS: r.upstreamLatencyMS,
		TTFTMS:            r.ttftMS,
		ClientIP:          r.metadata.ClientIP,
		UserAgent:         r.metadata.UserAgent,
		CreatedAt:         r.started.UTC(),
	}
	if output := usage.Get("output"); output > 0 && totalLatency > 0 {
		r.request.OutputTokensPerSec = float64(output) / (float64(totalLatency) / 1000)
	}
	if r.request.Usage.Meters == nil {
		r.request.Usage = model.NewUsage()
	}
	r.request.SettlementStatus = "none"
	for _, attempt := range r.attempts {
		if attempt != nil {
			r.request.SettlementStatus = model.MergeSettlementStatus(r.request.SettlementStatus, attempt.SettlementStatus)
		}
	}
	r.persist()
}

func (r *requestRecorder) persist() {
	if r == nil || r.service == nil || r.service.recordStore == nil || r.request == nil {
		return
	}
	request := r.request
	attempts := append([]*model.Attempt(nil), r.attempts...)
	store := r.service.recordStore
	for _, attempt := range attempts {
		if attempt != nil {
			attempt.RequestID = request.ID
		}
	}
	if err := r.service.writer.Submit(func(ctx context.Context) error {
		if err := store.InsertRequest(ctx, request); err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attempt == nil {
				continue
			}
			if err := store.InsertAttempt(ctx, attempt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		slog.Error("request persistence rejected", "request_id", request.ID)
	}
}

func finishAttempt(attempt *model.Attempt, status string, statusCode int, err error, usage model.Usage, started time.Time) {
	if attempt == nil {
		return
	}
	attempt.Status = status
	attempt.StatusCode = statusCode
	attempt.Usage = usage
	attempt.LatencyMS = nonNegativeDurationMS(time.Since(started))
	errorType, errorSummary, classifiedStatus := recordError(err)
	attempt.ErrorType = errorType
	attempt.ErrorSummary = errorSummary
	if attempt.StatusCode == 0 && classifiedStatus != 0 {
		attempt.StatusCode = classifiedStatus
	}
	if status == "success" {
		attempt.ErrorType = ""
		attempt.ErrorSummary = ""
		if attempt.StatusCode == 0 {
			attempt.StatusCode = http.StatusOK
		}
	}
	if attempt.Usage.Meters == nil {
		attempt.Usage = model.NewUsage()
	}
}

func (s *Service) settleAttempt(ctx context.Context, recorder *requestRecorder, attempt *model.Attempt, route resolvedRoute, usage model.Usage, success bool) {
	if s == nil || recorder == nil || attempt == nil {
		return
	}
	// Usage is independent of pricing, including explicit zero rates, and must
	// survive price/store failures without bypassing token accounting.
	policyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if policy, ok := s.policy.(UsagePolicy); ok {
		policy.ObserveUsage(policyCtx, recorder.metadata.UserID, recorder.metadata.APIKeyID, recorder.metadata.ModelAlias, usage)
	}
	if s.settlement == nil {
		return
	}
	settlementCtx := billing.WithAttempt(ctx, attempt.SettlementID, attempt.CreatedAt)
	result, err := s.settlement.Settle(settlementCtx, recorder.metadata.ID, recorder.metadata.UserID, recorder.metadata.APIKeyID, recorder.metadata.ModelAlias, route.providerModel, route.providerID, usage, success)
	if result != nil {
		attempt.SettlementStatus = result.SettlementStatus
	}
	if attempt.SettlementStatus == "" {
		attempt.SettlementStatus = "none"
	}
	if err != nil {
		attempt.SettlementError = "settlement_failed"
		attempt.SettlementStatus = "failed"
		slog.Error("attempt settlement failed", "request_id", recorder.metadata.ID, "settlement_id", attempt.SettlementID)
	}
	if result == nil {
		if policy, ok := s.policy.(settlementChargePolicy); ok {
			policy.RecordSettlementFailure(policyCtx, recorder.metadata.UserID)
		}
		return
	}
	if result.Unpriced {
		// Billable usage with no configured price version: no ledger row was
		// written. Record the misconfiguration instead of inventing a charge.
		attempt.SettlementError = "unpriced"
		if err == nil {
			attempt.SettlementStatus = "unpriced"
		}
		s.unpricedSettlements.Add(1)
		slog.Warn("attempt settled without a price version", "request_id", recorder.metadata.ID, "model_alias", recorder.metadata.ModelAlias)
	}
	if result.ZeroCreditMeters > 0 {
		// Missing necessary credit rates or positive rates quantized to 0.000
		// are pricing holes. Billing excludes explicit zero credit rates, even
		// when upstream cost is positive, from this diagnostic.
		s.zeroCreditSettlements.Add(1)
		slog.Warn("attempt settled with zero credits for billable usage", "request_id", recorder.metadata.ID, "model_alias", recorder.metadata.ModelAlias, "zero_credit_meters", result.ZeroCreditMeters)
	}
	attempt.Cost = result.Cost
	recorder.credits = recorder.credits.Add(result.Credits)
	if !result.Persisted {
		if policy, ok := s.policy.(settlementChargePolicy); ok {
			policy.RecordPendingSettlement(policyCtx, recorder.metadata.UserID, attempt.SettlementID, result.Cost, result.Credits)
		} else if policy, ok := s.policy.(ChargePolicy); ok {
			policy.RecordCharge(policyCtx, recorder.metadata.UserID, result.Cost, result.Credits)
		}
	}
}

func recordError(err error) (errorType, summary string, statusCode int) {
	if err == nil {
		return "", "", 0
	}
	if errors.Is(err, policy.ErrRateLimited) {
		return "rate_limit", "gateway rate limit exceeded", http.StatusTooManyRequests
	}
	if errors.Is(err, policy.ErrBudgetExceeded) {
		return "budget", "budget limit exceeded", http.StatusTooManyRequests
	}
	if errors.Is(err, ErrModelNotFound) {
		return "invalid_request", "model alias not found", http.StatusNotFound
	}
	if errors.Is(err, ErrNoRoute) {
		return "routing", "no active route for model alias", http.StatusServiceUnavailable
	}
	// A provider that cannot serve the requested operation is not an upstream
	// failure: the client receives 501 and the request record must agree with
	// that verdict instead of degrading to a generic 502.
	if errors.Is(err, ErrResponsesUnsupported) {
		return "unsupported", "provider does not implement the OpenAI Responses API", http.StatusNotImplemented
	}
	if errors.Is(err, adapter.ErrUnsupported) {
		return "unsupported", "the selected provider does not support this operation", http.StatusNotImplemented
	}

	// A request the adapter refuses locally because it cannot be represented is
	// the caller's 400, not an upstream failure. The sentinel is checked before
	// the upstream branch so a real upstream 400 (an *adapter.Error) keeps its
	// own "client" classification.
	if errors.Is(err, adapter.ErrInvalidRequest) {
		return "invalid_request", "request cannot be represented for the provider protocol", http.StatusBadRequest
	}
	var upstream *adapter.Error
	if errors.As(err, &upstream) {
		statusCode = upstream.StatusCode
		switch upstream.Type {
		case "connection":
			return "connection", "upstream connection failed", statusCode
		case "timeout":
			return "timeout", "upstream request timed out", statusCode
		case "rate_limit":
			return "rate_limit", "upstream rate limit exceeded", statusCode
		case "server":
			return "server", "upstream server error", statusCode
		case "auth":
			return "auth", "upstream authentication failed", statusCode
		case "client":
			return "client", "upstream rejected the request", statusCode
		default:
			return "upstream", "upstream request failed", statusCode
		}
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", "request canceled", http.StatusRequestTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "request timed out", http.StatusGatewayTimeout
	}
	return "gateway", "gateway request failed", 0
}

func nonNegativeDurationMS(duration time.Duration) int64 {
	milliseconds := duration.Milliseconds()
	if milliseconds < 0 {
		return 0
	}
	return milliseconds
}

func addUsage(total *model.Usage, next model.Usage) {
	if total == nil {
		return
	}
	*total = total.Add(next)
}

func usageFromChatResponse(response *protocol.ChatResponse) model.Usage {
	if response == nil {
		return model.NewUsage()
	}
	return response.Usage
}

func usageFromResponsesResponse(response *protocol.ResponsesResponse) model.Usage {
	if response == nil {
		return model.NewUsage()
	}
	return response.Usage
}

// failedAttemptUsage is the single billing rule shared by every failure path:
// trust usage reported by the upstream, otherwise estimate from output actually
// observed, otherwise record no usage at all. Adapters do not report usage for
// failed calls, so a non-streaming failure normally carries no Cost.
func failedAttemptUsage(observed model.Usage, inputBytes, outputBytes int) model.Usage {
	if len(observed.Meters) > 0 {
		return observed
	}
	if outputBytes > 0 {
		return estimateUsage(inputBytes, outputBytes)
	}
	return model.NewUsage()
}

// chatRequestBytes sums the text carried by a chat request. It is an input-size
// proxy for the coarse estimator, not a tokenizer.
func chatRequestBytes(req *protocol.ChatRequest) int {
	if req == nil {
		return 0
	}
	n := 0
	for _, message := range req.Messages {
		n += messageTextBytes(message)
	}
	return n
}

// responsesRequestBytes is the Responses equivalent of chatRequestBytes. The raw
// input is measured as-is so the estimate matches what the upstream received;
// when a caller built the request from normalized messages instead, those are
// measured so the input size is never silently zero.
func responsesRequestBytes(req *protocol.ResponsesRequest) int {
	if req == nil {
		return 0
	}
	if len(req.Input) == 0 {
		n := 0
		for _, message := range req.Messages {
			n += messageTextBytes(message)
		}
		return n + len(req.Instructions)
	}
	return jsonTextBytes(req.Input) + len(req.Instructions)
}

// estimateUsage is a coarse text-only fallback, not a provider tokenizer.
func estimateUsage(inputBytes, outputBytes int) model.Usage {
	u := model.NewUsage()
	if inputBytes == 0 && outputBytes == 0 {
		return u
	}
	if inputBytes > 0 {
		u.Meters["input"] = int64((inputBytes + 3) / 4)
	}
	if outputBytes > 0 {
		u.Meters["output"] = int64((outputBytes + 3) / 4)
	}
	u.Meters["total"] = u.Meters["input"] + u.Meters["output"]
	u.Estimated = true
	return u
}

func estimateChatUsage(req *protocol.ChatRequest, response *protocol.ChatResponse) model.Usage {
	input, output := chatRequestBytes(req), 0
	if response != nil {
		for _, choice := range response.Choices {
			output += messageTextBytes(choice.Message)
		}
	}
	return estimateUsage(input, output)
}

func estimateResponsesUsage(req *protocol.ResponsesRequest, response *protocol.ResponsesResponse) model.Usage {
	input, output := responsesRequestBytes(req), 0
	if response != nil {
		output = jsonTextBytes(response.Output)
	}
	return estimateUsage(input, output)
}

func hasReportedUsage(usage model.Usage) bool {
	return usage.Get("total") > 0 || usage.Get("input") > 0 || usage.Get("output") > 0
}

// toolCallDeltaBytes measures streamed tool-call fragments. A failure that
// already produced argument deltas did consume output tokens even though no
// complete tool call ever landed on the message.
func toolCallDeltaBytes(deltas []protocol.ToolCallDelta) int {
	n := 0
	for _, delta := range deltas {
		n += len(delta.ID) + len(delta.Name) + len(delta.Arguments)
	}
	return n
}

func messageTextBytes(message protocol.Message) int {
	// Only use visible text for the coarse fallback. Reported upstream usage
	// remains authoritative; reasoning_content is included only when emitted.
	n := len(message.ReasoningContent)
	for _, part := range message.Content {
		n += len(part.Text)
	}
	for _, call := range message.ToolCalls {
		n += len(call.Function.Arguments)
	}
	return n
}

func jsonTextBytes(raw json.RawMessage) int {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	return textValueBytes(value)
}

func textValueBytes(value any) int {
	switch v := value.(type) {
	case string:
		return len(v)
	case []any:
		n := 0
		for _, item := range v {
			n += textValueBytes(item)
		}
		return n
	case map[string]any:
		n := 0
		for _, key := range []string{"messages", "content", "text", "input", "output", "system", "instructions"} {
			n += textValueBytes(v[key])
		}
		return n
	default:
		return 0
	}
}
