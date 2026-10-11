package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/background"
	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
	"github.com/zjvtit/omni-gateway/internal/store"
)

var (
	ErrModelNotFound = errors.New("model alias not found")
	ErrNoRoute       = errors.New("no active route for model alias")
	// ErrResponsesUnsupported reports that no candidate route declares support for
	// the OpenAI Responses API. Providers opt in with `supports_responses`, so a
	// missing declaration is a statement about the deployment, not an upstream
	// fault; it wraps adapter.ErrUnsupported so the generic 501 semantics and the
	// existing error classification keep working.
	ErrResponsesUnsupported = fmt.Errorf("%w: provider does not implement the OpenAI Responses API", adapter.ErrUnsupported)
)

// RouteStore is the configuration surface needed to resolve an alias into
// ordered provider targets.
type RouteStore interface {
	GetAlias(context.Context, string) (*model.ModelAlias, error)
	ListAliases(context.Context) ([]*model.ModelAlias, error)
	ListRouteEntries(context.Context, int64) ([]*model.RouteEntry, error)
	GetProvider(context.Context, int64) (*model.Provider, error)
}

type providerHealthStore interface {
	MarkProviderCooldown(context.Context, int64, time.Time) error
	MarkProviderHealthy(context.Context, int64) error
}

// Service performs the minimum gateway orchestration needed by the public V1
// API: alias resolution, provider adapter creation, and ordered failover.
type Service struct {
	store           RouteStore
	cipher          *gatewaycrypto.Cipher
	client          *http.Client
	recordStore     RecordStore
	settlement      Settlement
	policy          RequestPolicy
	contentStore    ContentStore
	contentCipher   *gatewaycrypto.Cipher
	contentMaxBytes int
	contentEnabled  bool
	writer          *background.Writer
	routeMu         sync.Mutex
	routeStates     map[int64]*routeState
	// Process-local observability counters. They are bounded and monotonic and
	// are exported on the admin runtime endpoint; they never feed billing.
	unpricedSettlements   atomic.Uint64
	routeResolveFailures  atomic.Uint64
	zeroCreditSettlements atomic.Uint64
}

// RuntimeCounters is the process-local counter snapshot exposed by the admin
// runtime endpoint. It is observability only; the values reset on restart and
// are not shared between instances (see the V1 single-instance boundary).
type RuntimeCounters struct {
	// UnpricedSettlements counts attempts that had billable usage but no
	// matching price version, so no ledger row was written.
	UnpricedSettlements uint64 `json:"unpriced_settlements"`
	// ZeroCreditSettlements counts attempts where a meter that at least one ledger
	// prices still produced a 0.000 credit charge: the credit rate is missing while
	// upstream cost accrues, or the rate is below the three-decimal amount scale.
	// Those are pricing holes. A meter no price version rates (the reported
	// "total") is deliberately free and is not counted.
	ZeroCreditSettlements uint64 `json:"zero_credit_settlements"`
	// RouteResolveFailures counts routes skipped because their provider
	// configuration could not be decrypted or turned into an adapter.
	RouteResolveFailures uint64 `json:"route_resolve_failed"`
}

// NewService creates a gateway service. The cipher is only required when a
// configured provider has an encrypted API key.
func NewService(routeStore RouteStore, cipher *gatewaycrypto.Cipher, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{}
	}
	return &Service{store: routeStore, cipher: cipher, client: client, routeStates: make(map[int64]*routeState), writer: background.New(background.Options{})}
}

// NewServiceWithRecords creates a gateway service with best-effort request and
// attempt persistence enabled.
func NewServiceWithRecords(routeStore RouteStore, cipher *gatewaycrypto.Cipher, client *http.Client, recordStore RecordStore) *Service {
	service := NewService(routeStore, cipher, client)
	service.recordStore = recordStore
	return service
}

// NewServiceWithDependencies creates a fully wired service for the monolith.
func NewServiceWithDependencies(routeStore RouteStore, cipher *gatewaycrypto.Cipher, client *http.Client, recordStore RecordStore, settlement Settlement, policy RequestPolicy) *Service {
	service := NewServiceWithRecords(routeStore, cipher, client, recordStore)
	service.settlement = settlement
	service.policy = policy
	return service
}

// RuntimeCounters returns a snapshot of the process-local observability
// counters. It is safe to call concurrently.
func (s *Service) RuntimeCounters() RuntimeCounters {
	if s == nil {
		return RuntimeCounters{}
	}
	return RuntimeCounters{
		UnpricedSettlements:   s.unpricedSettlements.Load(),
		ZeroCreditSettlements: s.zeroCreditSettlements.Load(),
		RouteResolveFailures:  s.routeResolveFailures.Load(),
	}
}

// SetSettlement attaches the optional pricing and ledger settlement service.
func (s *Service) SetSettlement(settlement Settlement) {
	if s != nil {
		s.settlement = settlement
	}
}

// SetPolicy attaches optional budget and rate-limit admission checks.
func (s *Service) SetPolicy(policy RequestPolicy) {
	if s != nil {
		s.policy = policy
	}
}

// SetContentLogging enables optional encrypted content logging.
func (s *Service) SetContentLogging(contentStore ContentStore, cipher *gatewaycrypto.Cipher, maxBytes int, enabled bool) {
	if s == nil {
		return
	}
	s.contentStore = contentStore
	s.contentCipher = cipher
	s.contentMaxBytes = maxBytes
	s.contentEnabled = enabled && contentStore != nil && cipher != nil && maxBytes > 0
}

// ListModels returns active model aliases. The HTTP layer applies the calling
// API key's model allow-list after this configuration lookup.
func (s *Service) ListModels(ctx context.Context) ([]string, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("gateway store is not configured")
	}
	aliases, err := s.store.ListAliases(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if alias != nil && alias.Active {
			models = append(models, alias.Name)
		}
	}
	sort.Strings(models)
	return models, nil
}

// Chat calls the first successful route and retries only errors classified by
// the adapter as safe for failover. Each upstream call is recorded when the
// HTTP caller supplied request metadata and a RecordStore is configured.
func (s *Service) Chat(ctx context.Context, req *protocol.ChatRequest) (*protocol.ChatResponse, error) {
	if req == nil {
		return nil, errors.New("nil chat request")
	}
	totalStarted := time.Now()
	recorder := newRequestRecorder(s, ctx)
	release, policyErr := s.acquirePolicy(ctx, req.Model)
	if policyErr != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, policyErr, model.NewUsage(), totalStarted)
		}
		return nil, policyErr
	}
	defer release()
	routes, err := s.resolveRoutes(ctx, req.Model)
	if err != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, err, model.NewUsage(), totalStarted)
		}
		return nil, err
	}
	var lastErr error
	for _, route := range routes {
		attemptStarted := time.Now()
		attempt := recorder.attempt(route, attemptStarted)
		upstreamReq := *req
		upstreamReq.Model = route.providerModel
		upstreamReq.CompatibilityProfile = string(route.compatibilityProfile)
		upstreamReq.ModelSupportsReasoning = route.modelSupportsReasoning
		if validationErr := validateChatRoute(route, &upstreamReq); validationErr != nil {
			if recorder != nil {
				recorder.finish(model.StatusError, 0, validationErr, model.NewUsage(), totalStarted)
			}
			return nil, validationErr
		}
		releaseRoute, routeErr := s.acquireRoute(ctx, route)
		if routeErr != nil {
			lastErr = routeErr
			continue
		}
		response, callErr := route.adapter.Chat(ctx, &upstreamReq)
		releaseRoute()
		if callErr == nil && response == nil {
			callErr = errors.New("upstream returned an empty response")
		}
		if callErr == nil {
			s.markProviderHealthy(ctx, route.providerID)
			response.Model = req.Model
			usage := usageFromChatResponse(response)
			if !hasReportedUsage(usage) {
				usage = estimateChatUsage(req, response)
			}
			s.settleAttempt(ctx, recorder, attempt, route, usage, true)
			s.persistContent(ctx, req, response)
			finishAttempt(attempt, "success", http.StatusOK, nil, usage, attemptStarted)
			if recorder != nil {
				recorder.addAttempt(attempt)
				recorder.finish(model.StatusSuccess, http.StatusOK, nil, usage, totalStarted)
			}
			return response, nil
		}
		failureUsage := failedAttemptUsage(model.NewUsage(), chatRequestBytes(req), 0)
		s.settleAttempt(ctx, recorder, attempt, route, failureUsage, false)
		finishAttempt(attempt, "error", 0, callErr, failureUsage, attemptStarted)
		if recorder != nil {
			recorder.addAttempt(attempt)
		}
		lastErr = callErr
		s.markProviderFailure(ctx, route.providerID, callErr)
		if !isRetryable(callErr) {
			if recorder != nil {
				recorder.finish(model.StatusError, 0, callErr, model.NewUsage(), totalStarted)
			}
			return nil, callErr
		}
	}
	if recorder != nil {
		recorder.finish(model.StatusError, 0, lastErr, model.NewUsage(), totalStarted)
	}
	return nil, lastErr
}

// ChatStream proxies a stream through the first route that can start without
// emitting an event. Once any event has reached the caller, failover is
// disabled to avoid concatenating two independent completions. Stream usage
// and every attempted upstream route are recorded when configured.
func (s *Service) ChatStream(ctx context.Context, req *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error {
	if req == nil {
		return errors.New("nil chat request")
	}
	if emit == nil {
		return errors.New("nil stream event callback")
	}
	totalStarted := time.Now()
	recorder := newRequestRecorder(s, ctx)
	release, policyErr := s.acquirePolicy(ctx, req.Model)
	if policyErr != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, policyErr, model.NewUsage(), totalStarted)
		}
		return policyErr
	}
	defer release()
	routes, err := s.resolveRoutes(ctx, req.Model)
	if err != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, err, model.NewUsage(), totalStarted)
		}
		return err
	}
	var lastErr error
	streamEvents := ""
	for _, route := range routes {
		attemptStarted := time.Now()
		attempt := recorder.attempt(route, attemptStarted)
		attemptUsage := model.NewUsage()
		outputBytes := 0
		firstEventAt := time.Time{}
		upstreamReq := *req
		upstreamReq.Model = route.providerModel
		upstreamReq.CompatibilityProfile = string(route.compatibilityProfile)
		upstreamReq.ModelSupportsReasoning = route.modelSupportsReasoning
		if validationErr := validateChatRoute(route, &upstreamReq); validationErr != nil {
			if recorder != nil {
				recorder.finish(model.StatusError, 0, validationErr, model.NewUsage(), totalStarted)
			}
			return validationErr
		}
		releaseRoute, routeErr := s.acquireRoute(ctx, route)
		if routeErr != nil {
			lastErr = routeErr
			continue
		}
		emitted := false
		completed := false
		callErr := route.adapter.ChatStream(ctx, &upstreamReq, func(event protocol.StreamEvent) error {
			outputBytes += messageTextBytes(event.Delta)
			if event.Type == "error" {
				return &adapter.Error{Type: "server", StatusCode: 502, Message: "upstream stream failed"}
			}
			if event.Type == "done" {
				completed = true
			}
			emitted = true
			if s.contentEnabled {
				streamEvents = captureStreamContent(streamEvents, event, s.contentMaxBytes)
			}
			if event.Type == "chunk" && firstEventAt.IsZero() {
				firstEventAt = time.Now()
				attempt.TTFTMS = nonNegativeDurationMS(time.Since(attemptStarted))
			}
			if event.Type == "usage" {
				attemptUsage = attemptUsage.Add(event.Usage)
			}
			return emit(event)
		})
		releaseRoute()
		if callErr == nil && !completed {
			callErr = &adapter.Error{Type: "server", StatusCode: 502, Message: "upstream stream ended before completion"}
		}
		inputBytes := chatRequestBytes(req)
		if !hasReportedUsage(attemptUsage) && callErr == nil {
			// A stream that completed without reported usage still charges the
			// estimated input.
			attemptUsage = estimateUsage(inputBytes, outputBytes)
		} else if callErr != nil {
			// Failures share one rule across every attempt path.
			attemptUsage = failedAttemptUsage(attemptUsage, inputBytes, outputBytes)
		}
		if callErr == nil {
			s.markProviderHealthy(ctx, route.providerID)
			s.settleAttempt(ctx, recorder, attempt, route, attemptUsage, true)
			s.persistContent(ctx, req, streamEvents)
			finishAttempt(attempt, "success", http.StatusOK, nil, attemptUsage, attemptStarted)
			if recorder != nil {
				recorder.addAttempt(attempt)
				recorder.finish(model.StatusSuccess, http.StatusOK, nil, attemptUsage, totalStarted)
			}
			return nil
		}
		s.settleAttempt(ctx, recorder, attempt, route, attemptUsage, false)
		finishAttempt(attempt, "error", 0, callErr, attemptUsage, attemptStarted)
		if recorder != nil {
			recorder.addAttempt(attempt)
		}
		lastErr = callErr
		s.markProviderFailure(ctx, route.providerID, callErr)
		if emitted || !isRetryable(callErr) {
			if recorder != nil {
				s.persistContent(ctx, req, streamEvents)
				recorder.finish(model.StatusError, 0, callErr, attemptUsage, totalStarted)
			}
			return callErr
		}
	}
	if recorder != nil {
		recorder.finish(model.StatusError, 0, lastErr, model.NewUsage(), totalStarted)
	}
	return lastErr
}

// Responses calls the first successful route for the OpenAI Responses API and
// records the request/attempt lifecycle when metadata and a RecordStore exist.
func (s *Service) Responses(ctx context.Context, req *protocol.ResponsesRequest) (*protocol.ResponsesResponse, error) {
	if req == nil {
		return nil, errors.New("nil Responses request")
	}
	totalStarted := time.Now()
	recorder := newRequestRecorder(s, ctx)
	release, policyErr := s.acquirePolicy(ctx, req.Model)
	if policyErr != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, policyErr, model.NewUsage(), totalStarted)
		}
		return nil, policyErr
	}
	defer release()
	routes, err := s.resolveRoutes(ctx, req.Model)
	if err != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, err, model.NewUsage(), totalStarted)
		}
		return nil, err
	}
	routes, err = responsesRoutes(routes)
	if err != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, err, model.NewUsage(), totalStarted)
		}
		return nil, err
	}
	var lastErr error
	for _, route := range routes {
		attemptStarted := time.Now()
		attempt := recorder.attempt(route, attemptStarted)
		upstreamReq := *req
		upstreamReq.Model = route.providerModel
		releaseRoute, routeErr := s.acquireRoute(ctx, route)
		if routeErr != nil {
			lastErr = routeErr
			continue
		}
		response, callErr := route.adapter.Responses(ctx, &upstreamReq)
		releaseRoute()
		if callErr == nil && response == nil {
			callErr = errors.New("upstream returned an empty Responses response")
		}
		if callErr == nil {
			s.markProviderHealthy(ctx, route.providerID)
			response.Model = req.Model
			usage := usageFromResponsesResponse(response)
			if !hasReportedUsage(usage) {
				usage = estimateResponsesUsage(req, response)
			}
			s.settleAttempt(ctx, recorder, attempt, route, usage, true)
			s.persistContent(ctx, req, response)
			finishAttempt(attempt, "success", http.StatusOK, nil, usage, attemptStarted)
			if recorder != nil {
				recorder.addAttempt(attempt)
				recorder.finish(model.StatusSuccess, http.StatusOK, nil, usage, totalStarted)
			}
			return response, nil
		}
		failureUsage := failedAttemptUsage(model.NewUsage(), responsesRequestBytes(req), 0)
		s.settleAttempt(ctx, recorder, attempt, route, failureUsage, false)
		finishAttempt(attempt, "error", 0, callErr, failureUsage, attemptStarted)
		if recorder != nil {
			recorder.addAttempt(attempt)
		}
		lastErr = callErr
		s.markProviderFailure(ctx, route.providerID, callErr)
		if !errors.Is(callErr, adapter.ErrUnsupported) && !isRetryable(callErr) {
			if recorder != nil {
				recorder.finish(model.StatusError, 0, callErr, model.NewUsage(), totalStarted)
			}
			return nil, callErr
		}
	}
	if recorder != nil {
		recorder.finish(model.StatusError, 0, lastErr, model.NewUsage(), totalStarted)
	}
	return nil, lastErr
}

// ResponsesStream proxies a Responses stream and only fails over before the
// first upstream event is emitted. Stream usage and attempts are recorded when
// the HTTP caller supplied request metadata.
func (s *Service) ResponsesStream(ctx context.Context, req *protocol.ResponsesRequest, emit func(protocol.StreamEvent) error) error {
	if req == nil {
		return errors.New("nil Responses request")
	}
	if emit == nil {
		return errors.New("nil stream event callback")
	}
	totalStarted := time.Now()
	recorder := newRequestRecorder(s, ctx)
	release, policyErr := s.acquirePolicy(ctx, req.Model)
	if policyErr != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, policyErr, model.NewUsage(), totalStarted)
		}
		return policyErr
	}
	defer release()
	routes, err := s.resolveRoutes(ctx, req.Model)
	if err != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, err, model.NewUsage(), totalStarted)
		}
		return err
	}
	routes, err = responsesRoutes(routes)
	if err != nil {
		if recorder != nil {
			recorder.finish(model.StatusError, 0, err, model.NewUsage(), totalStarted)
		}
		return err
	}
	var lastErr error
	streamEvents := ""
	for _, route := range routes {
		attemptStarted := time.Now()
		attempt := recorder.attempt(route, attemptStarted)
		attemptUsage := model.NewUsage()
		outputBytes := 0
		firstEventAt := time.Time{}
		upstreamReq := *req
		upstreamReq.Model = route.providerModel
		releaseRoute, routeErr := s.acquireRoute(ctx, route)
		if routeErr != nil {
			lastErr = routeErr
			continue
		}
		emitted := false
		completed := false
		callErr := route.adapter.ResponsesStream(ctx, &upstreamReq, func(event protocol.StreamEvent) error {
			// Streamed tool-call fragments live in event.ToolCalls while adapters
			// that emit whole tool calls put them inside the delta, so both are
			// counted and neither is double counted.
			outputBytes += messageTextBytes(event.Delta) + toolCallDeltaBytes(event.ToolCalls)
			if event.Type == "error" {
				// Before any output, keep the existing safe retry/JSON-error path.
				// Once a Responses event has reached the client, preserve the
				// protocol's terminal response.failed frame instead of replacing
				// it with a generic error event. The attempt still fails for
				// settlement and must never switch upstream after this point.
				if emitted && event.Event == "response.failed" && len(event.Raw) > 0 {
					if err := emit(event); err != nil {
						return err
					}
				}
				return &adapter.Error{Type: "server", StatusCode: 502, Message: "upstream stream failed"}
			}
			if event.Type == "done" {
				completed = true
			}
			emitted = true
			if s.contentEnabled {
				streamEvents = captureStreamContent(streamEvents, event, s.contentMaxBytes)
			}
			if event.Type == "chunk" && firstEventAt.IsZero() {
				firstEventAt = time.Now()
				attempt.TTFTMS = nonNegativeDurationMS(time.Since(attemptStarted))
			}
			if event.Type == "usage" {
				attemptUsage = attemptUsage.Add(event.Usage)
			}
			return emit(event)
		})
		releaseRoute()
		if callErr == nil && !completed {
			callErr = &adapter.Error{Type: "server", StatusCode: 502, Message: "upstream stream ended before completion"}
		}
		inputBytes := responsesRequestBytes(req)
		if !hasReportedUsage(attemptUsage) && callErr == nil {
			attemptUsage = estimateUsage(inputBytes, outputBytes)
		} else if callErr != nil {
			attemptUsage = failedAttemptUsage(attemptUsage, inputBytes, outputBytes)
		}
		if callErr == nil {
			s.markProviderHealthy(ctx, route.providerID)
			s.settleAttempt(ctx, recorder, attempt, route, attemptUsage, true)
			s.persistContent(ctx, req, streamEvents)
			finishAttempt(attempt, "success", http.StatusOK, nil, attemptUsage, attemptStarted)
			if recorder != nil {
				recorder.addAttempt(attempt)
				recorder.finish(model.StatusSuccess, http.StatusOK, nil, attemptUsage, totalStarted)
			}
			return nil
		}
		s.settleAttempt(ctx, recorder, attempt, route, attemptUsage, false)
		finishAttempt(attempt, "error", 0, callErr, attemptUsage, attemptStarted)
		if recorder != nil {
			recorder.addAttempt(attempt)
		}
		lastErr = callErr
		s.markProviderFailure(ctx, route.providerID, callErr)
		if emitted || (!errors.Is(callErr, adapter.ErrUnsupported) && !isRetryable(callErr)) {
			if recorder != nil {
				s.persistContent(ctx, req, streamEvents)
				recorder.finish(model.StatusError, 0, callErr, attemptUsage, totalStarted)
			}
			return callErr
		}
	}
	if recorder != nil {
		recorder.finish(model.StatusError, 0, lastErr, model.NewUsage(), totalStarted)
	}
	return lastErr
}

type resolvedRoute struct {
	providerID             int64
	providerModel          string
	priority               int
	adapter                adapter.Adapter
	maxConcurrency         int
	supportsResponses      bool
	compatibilityProfile   model.ProviderCompatibilityProfile
	modelSupportsReasoning bool
}

func (s *Service) resolveRoutes(ctx context.Context, aliasName string) ([]resolvedRoute, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("gateway store is not configured")
	}
	if aliasName == "" {
		return nil, ErrModelNotFound
	}
	alias, err := s.store.GetAlias(ctx, aliasName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrModelNotFound
		}
		return nil, err
	}
	if alias == nil || !alias.Active {
		return nil, ErrModelNotFound
	}
	entries, err := s.store.ListRouteEntries(ctx, alias.ID)
	if err != nil {
		return nil, err
	}
	resolved := make([]resolvedRoute, 0, len(entries))
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i] == nil {
			return false
		}
		if entries[j] == nil {
			return true
		}
		if entries[i].Priority == entries[j].Priority {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].Priority < entries[j].Priority
	})
	// A single broken provider configuration must not take the whole alias
	// down: the route is skipped, counted, logged, and the next one is tried.
	// The last resolution error is returned only when nothing resolved at all.
	var lastResolveErr error
	for _, entry := range entries {
		if entry == nil || !entry.Active || entry.ProviderModel == "" {
			continue
		}
		provider, err := s.store.GetProvider(ctx, entry.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("load provider %d: %w", entry.ProviderID, err)
		}
		if provider == nil || !provider.Active {
			continue
		}
		s.routeMu.Lock()
		if state := s.routeStates[provider.ID]; state != nil && provider.LastVerifiedAt != nil && provider.LastVerifiedAt.After(state.failedAt) && provider.HealthStatus == "available" {
			state.until = time.Time{}
			state.failures = 0
		}
		s.routeMu.Unlock()
		if provider.HealthStatus == "cooldown" && provider.CooldownUntil != nil && provider.CooldownUntil.After(time.Now()) {
			continue
		}
		upstreamKey, err := s.decryptProviderKey(provider)
		if err != nil {
			lastResolveErr = fmt.Errorf("decrypt provider %d credentials: %w", provider.ID, err)
			s.routeResolveFailures.Add(1)
			slog.Error("skipping route: provider credentials could not be decrypted", "entry_id", entry.ID, "provider_id", provider.ID, "error", err)
			continue
		}
		upstreamAdapter, err := newAdapter(provider, upstreamKey, s.client)
		if err != nil {
			lastResolveErr = fmt.Errorf("build adapter for provider %d: %w", provider.ID, err)
			s.routeResolveFailures.Add(1)
			slog.Error("skipping route: provider adapter could not be built", "entry_id", entry.ID, "provider_id", provider.ID, "error", err)
			continue
		}
		// Capabilities belong to the actual provider model, not to an alias.
		// Older in-memory RouteStore implementations may not expose model records;
		// in that case reasoning is conservatively disabled.
		supportsReasoning := false
		if provider.CompatibilityProfile == model.ProviderCompatibilityDeepSeek {
			if models, ok := s.store.(interface {
				GetProviderModel(context.Context, int64, string) (*model.ProviderModel, error)
			}); ok {
				providerModel, lookupErr := models.GetProviderModel(ctx, provider.ID, entry.ProviderModel)
				if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
					lastResolveErr = fmt.Errorf("load provider model %d/%s: %w", provider.ID, entry.ProviderModel, lookupErr)
					s.routeResolveFailures.Add(1)
					continue
				}
				supportsReasoning = providerModel != nil && providerModel.Active && providerModel.Capabilities.Reasoning
			}
		}
		resolved = append(resolved, resolvedRoute{providerID: provider.ID, providerModel: entry.ProviderModel, priority: entry.Priority, adapter: upstreamAdapter, maxConcurrency: provider.MaxConcurrency, supportsResponses: provider.SupportsResponses, compatibilityProfile: provider.CompatibilityProfile, modelSupportsReasoning: supportsReasoning})
	}
	if len(resolved) == 0 {
		if lastResolveErr != nil {
			return nil, lastResolveErr
		}
		return nil, ErrNoRoute
	}
	return resolved, nil
}

// validateChatRoute rejects fields that a cross-protocol Chat conversion would
// otherwise silently discard. This check runs after route selection but before
// acquiring a connection, so an unsupported request is never sent upstream.
// OpenAI-compatible routes keep same-protocol extensions in rebuilt JSON.
func validateChatRoute(route resolvedRoute, req *protocol.ChatRequest) error {
	if route.adapter.Type() == string(model.ProviderOpenAICompat) {
		return nil
	}
	invalid := func(field string) error {
		return fmt.Errorf("%w: %s cannot be converted for this provider", adapter.ErrInvalidRequest, field)
	}
	if len(req.Extra) != 0 {
		return invalid("chat extension fields")
	}
	if req.ReasoningEffort != "" {
		return invalid("reasoning_effort")
	}
	if req.User != "" {
		return invalid("user")
	}
	if route.adapter.Type() == string(model.ProviderAnthropic) {
		if req.Temperature != nil || req.TopP != nil || len(req.Stop) != 0 || len(req.ResponseFormat) != 0 {
			return invalid("chat generation options")
		}
	} else if len(req.ResponseFormat) != 0 {
		var format map[string]json.RawMessage
		var kind string
		if json.Unmarshal(req.ResponseFormat, &format) != nil || len(format) != 1 || json.Unmarshal(format["type"], &kind) != nil || kind != "json_object" {
			return invalid("response_format")
		}
	}
	for _, tool := range req.Tools {
		if tool.Type != "function" && tool.Type != "" {
			return invalid("non-function tools")
		}
		if len(tool.Raw) > 0 {
			var fields map[string]json.RawMessage
			var function map[string]json.RawMessage
			if json.Unmarshal(tool.Raw, &fields) != nil || json.Unmarshal(fields["function"], &function) != nil || len(fields) > 2 || len(function) > 3 {
				return invalid("tool extension fields")
			}
			for key := range fields {
				if key != "type" && key != "function" {
					return invalid("tool extension fields")
				}
			}
			for key := range function {
				if key != "name" && key != "description" && key != "parameters" {
					return invalid("tool extension fields")
				}
			}
		}
	}
	for _, message := range req.Messages {
		if len(message.Extra) != 0 || message.Name != "" || message.ReasoningContent != "" {
			return invalid("message extension or reasoning fields")
		}
		for _, call := range message.ToolCalls {
			if call.Type != "function" && call.Type != "" {
				return invalid("non-function tool calls")
			}
			var arguments any
			if call.Function.Arguments != "" && json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil {
				return invalid("tool call arguments")
			}
			if len(call.Raw) > 0 {
				var fields map[string]json.RawMessage
				var function map[string]json.RawMessage
				if json.Unmarshal(call.Raw, &fields) != nil || json.Unmarshal(fields["function"], &function) != nil || len(fields) > 3 || len(function) > 2 {
					return invalid("tool call extension fields")
				}
				for key := range fields {
					if key != "id" && key != "type" && key != "function" {
						return invalid("tool call extension fields")
					}
				}
				for key := range function {
					if key != "name" && key != "arguments" {
						return invalid("tool call extension fields")
					}
				}
			}
		}
		for _, part := range message.Content {
			if part.Type != "text" && part.Type != "output_text" {
				// Remote image URLs and unknown part types cannot be represented by
				// these adapters without losing content or provider options.
				return invalid("content part")
			}
			if len(part.Raw) > 0 {
				var fields map[string]json.RawMessage
				if json.Unmarshal(part.Raw, &fields) != nil || len(fields) != 2 || !json.Valid(fields["text"]) || !json.Valid(fields["type"]) {
					return invalid("content part extension fields")
				}
			}
		}
	}
	return nil
}

// responsesRoutes keeps only the routes whose provider declares Responses API
// support. Refusing here, before any upstream call, is what turns an
// unsupported deployment into an explicit 501 instead of a 502.
func responsesRoutes(routes []resolvedRoute) ([]resolvedRoute, error) {
	supported := make([]resolvedRoute, 0, len(routes))
	for _, route := range routes {
		if route.supportsResponses {
			supported = append(supported, route)
		}
	}
	if len(supported) == 0 {
		return nil, ErrResponsesUnsupported
	}
	return supported, nil
}

func (s *Service) decryptProviderKey(provider *model.Provider) (string, error) {
	if provider.APIKeyEnc == "" {
		return "", nil
	}
	if s.cipher == nil {
		return "", errors.New("provider credentials require a configured master cipher")
	}
	return s.cipher.Decrypt(provider.APIKeyEnc)
}

func newAdapter(provider *model.Provider, apiKey string, client *http.Client) (adapter.Adapter, error) {
	if client == nil {
		client = &http.Client{}
	}
	if provider == nil {
		return nil, errors.New("nil provider")
	}
	timeoutMS := provider.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = 30_000
	}
	providerClient := *client
	providerClient.Timeout = time.Duration(timeoutMS) * time.Millisecond
	switch provider.Type {
	case model.ProviderOpenAICompat:
		return adapter.NewOpenAIAdapter(adapter.Config{BaseURL: provider.BaseURL, APIKey: apiKey, ExtraHeader: provider.ExtraHeader, TimeoutMS: timeoutMS, HTTPClient: &providerClient}), nil
	case model.ProviderAnthropic:
		return adapter.NewAnthropicAdapter(adapter.Config{BaseURL: provider.BaseURL, APIKey: apiKey, ExtraHeader: provider.ExtraHeader, TimeoutMS: timeoutMS, HTTPClient: &providerClient}), nil
	case model.ProviderGemini:
		return adapter.NewGeminiAdapter(adapter.Config{BaseURL: provider.BaseURL, APIKey: apiKey, ExtraHeader: provider.ExtraHeader, TimeoutMS: timeoutMS, HTTPClient: &providerClient}), nil
	default:
		return nil, fmt.Errorf("provider %q uses unsupported adapter type %q", provider.Name, provider.Type)
	}
}

func isRetryable(err error) bool {
	var upstream *adapter.Error
	return errors.As(err, &upstream) && upstream.IsRetryable()
}
