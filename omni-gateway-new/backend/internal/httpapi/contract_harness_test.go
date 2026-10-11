package httpapi

// Shared harness for the /v1/models, /v1/embeddings and /v1/images/generations
// HTTP contract tests. The requests traverse the real gateway service, alias
// resolution, adapter construction and native proxying, so the assertions cover
// the observable HTTP contract rather than a hand-written service double.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/billing"
	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	llmgateway "github.com/zjvtit/omni-gateway/internal/gateway"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

const (
	contractClientSecret   = "sk-contract-client"
	contractUpstreamSecret = "sk-contract-upstream"
)

// contractRouteStore is an in-memory gateway.RouteStore. It mirrors the store
// behavior the handlers rely on: an unknown alias resolves to ErrModelNotFound
// and a database failure stays a distinct error so the HTTP layer can classify
// it as a gateway failure.
type contractRouteStore struct {
	aliases   map[string]*model.ModelAlias
	entries   map[int64][]*model.RouteEntry
	providers map[int64]*model.Provider
	aliasErr  error
}

func (s *contractRouteStore) GetAlias(_ context.Context, name string) (*model.ModelAlias, error) {
	if s.aliasErr != nil {
		return nil, s.aliasErr
	}
	alias, ok := s.aliases[name]
	if !ok {
		return nil, llmgateway.ErrModelNotFound
	}
	return alias, nil
}

func (s *contractRouteStore) ListAliases(context.Context) ([]*model.ModelAlias, error) {
	if s.aliasErr != nil {
		return nil, s.aliasErr
	}
	aliases := make([]*model.ModelAlias, 0, len(s.aliases))
	for _, alias := range s.aliases {
		aliases = append(aliases, alias)
	}
	return aliases, nil
}

func (s *contractRouteStore) ListRouteEntries(_ context.Context, aliasID int64) ([]*model.RouteEntry, error) {
	return append([]*model.RouteEntry(nil), s.entries[aliasID]...), nil
}

func (s *contractRouteStore) GetProvider(_ context.Context, id int64) (*model.Provider, error) {
	provider, ok := s.providers[id]
	if !ok {
		return nil, llmgateway.ErrModelNotFound
	}
	return provider, nil
}

// contractAPIKeys returns an active gateway key with the requested allow-list.
func contractAPIKeys(modelAllow ...string) (*fakeAPIKeyStore, string) {
	return &fakeAPIKeyStore{
		key:  &model.APIKey{ID: 7, UserID: 3, KeyHash: hashAPIKeyForTest(contractClientSecret), Active: true, ModelAllow: modelAllow},
		user: &model.User{ID: 3, Username: "contract-user", Active: true},
	}, contractClientSecret
}

// contractProvider builds an OpenAI-compatible provider whose credential is
// encrypted with the same master key the harness service uses, so the upstream
// request must carry the provider secret instead of the caller's gateway key.
func contractProvider(t *testing.T, upstreamURL string) *model.Provider {
	t.Helper()
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("build contract cipher: %v", err)
	}
	encrypted, err := cipher.Encrypt(contractUpstreamSecret)
	if err != nil {
		t.Fatalf("encrypt contract upstream key: %v", err)
	}
	return &model.Provider{
		ID:           1,
		Name:         "contract-mock",
		Type:         model.ProviderOpenAICompat,
		BaseURL:      upstreamURL,
		APIKeyEnc:    encrypted,
		Active:       true,
		TimeoutMS:    10_000,
		HealthStatus: "available",
	}
}

// singleProviderStore maps one alias to one provider model on one mock upstream.
func singleProviderStore(t *testing.T, alias, providerModel, upstreamURL string) *contractRouteStore {
	t.Helper()
	return &contractRouteStore{
		aliases: map[string]*model.ModelAlias{alias: {ID: 1, Name: alias, Active: true}},
		entries: map[int64][]*model.RouteEntry{1: {
			{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: providerModel, Priority: 10, Active: true},
		}},
		providers: map[int64]*model.Provider{1: contractProvider(t, upstreamURL)},
	}
}

// newContractService wires the real gateway service (alias resolution, adapter
// construction, native proxying and usage parsing) over an in-memory store.
func newContractService(t *testing.T, store *contractRouteStore, client *http.Client) *llmgateway.Service {
	t.Helper()
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("build contract cipher: %v", err)
	}
	return llmgateway.NewService(store, cipher, client)
}

// newContractHandler wires the real gateway service behind the production HTTP
// router with the given in-memory route store and API-key store.
func newContractHandler(t *testing.T, store *contractRouteStore, keys APIKeyStore, client *http.Client) http.Handler {
	t.Helper()
	return NewHandlerWithGateway(nil, nil, keys, newContractService(t, store, client), AuthConfig{})
}

// contractSettlement captures the normalized usage the gateway reports for each
// attempt, proving the native response usage is parsed for metering.
type contractSettlement struct {
	mu      sync.Mutex
	aliases []string
	usages  []model.Usage
	success []bool
}

func (s *contractSettlement) Settle(_ context.Context, _ string, _, _ int64, modelAlias, _ string, _ int64, usage model.Usage, success bool) (*billing.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aliases = append(s.aliases, modelAlias)
	s.usages = append(s.usages, usage)
	s.success = append(s.success, success)
	return nil, nil
}

func (s *contractSettlement) last() (model.Usage, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.usages) == 0 {
		return model.NewUsage(), "", false
	}
	return s.usages[len(s.usages)-1], s.aliases[len(s.aliases)-1], s.success[len(s.success)-1]
}

// countingFlushRecorder proves the streaming handler flushes every frame it
// writes instead of buffering the whole SSE response until completion.
type countingFlushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *countingFlushRecorder) Flush() {
	f.flushes++
	f.ResponseRecorder.Flush()
}

// contractUpstreamCall captures one request that reached the mock upstream.
type contractUpstreamCall struct {
	Method        string
	Path          string
	Authorization string
	Accept        string
	ContentType   string
	Body          []byte
}

// contractUpstream records the mock upstream calls and serves the supplied
// handler. Tests read it back to prove what the gateway actually forwarded.
type contractUpstream struct {
	server  *httptest.Server
	mu      sync.Mutex
	calls   []contractUpstreamCall
	handler http.HandlerFunc
}

func newContractUpstream(handler http.HandlerFunc) *contractUpstream {
	upstream := &contractUpstream{handler: handler}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstream.mu.Lock()
		upstream.calls = append(upstream.calls, contractUpstreamCall{
			Method:        r.Method,
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			Accept:        r.Header.Get("Accept"),
			ContentType:   r.Header.Get("Content-Type"),
			Body:          body,
		})
		upstream.mu.Unlock()
		upstream.handler(w, r)
	}))
	return upstream
}

func (u *contractUpstream) Close() {
	if u != nil && u.server != nil {
		u.server.Close()
	}
}

func (u *contractUpstream) client() *http.Client { return u.server.Client() }

func (u *contractUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *contractUpstream) lastCall(t *testing.T) contractUpstreamCall {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.calls) == 0 {
		t.Fatal("mock upstream received no request")
	}
	return u.calls[len(u.calls)-1]
}

// gatewayRequest performs one authenticated data-plane request against the
// production handler.
func gatewayRequest(t *testing.T, handler http.Handler, method, path, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(recorder, request)
	return recorder
}

// decodeErrorEnvelope asserts the shared error envelope and returns its fields.
func decodeErrorEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) (status int, errorType, code, message string) {
	t.Helper()
	var payload struct {
		Error struct {
			Message   string `json:"message"`
			Type      string `json:"type"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, recorder.Body.String())
	}
	return recorder.Code, payload.Error.Type, payload.Error.Code, payload.Error.Message
}

// contractStubService is a direct NativeService double used to pin the HTTP
// layer's own obligations (invalid upstream payloads, per-frame flushing and
// failed-event semantics) without depending on adapter internals.
type contractStubService struct {
	fakeChatService
	nativeResponse *protocol.NativeResponse
	nativeErr      error
	nativeCalls    int
	events         []protocol.NativeEvent
	streamErr      error
	streamCalls    int
	streamCancels  int
}

func (s *contractStubService) Native(_ context.Context, _ *protocol.NativeRequest) (*protocol.NativeResponse, error) {
	s.nativeCalls++
	return s.nativeResponse, s.nativeErr
}

func (s *contractStubService) NativeStream(_ context.Context, _ *protocol.NativeRequest, emit func(protocol.NativeEvent) error) error {
	s.streamCalls++
	for _, event := range s.events {
		if err := emit(event); err != nil {
			s.streamCancels++
			return err
		}
	}
	return s.streamErr
}
