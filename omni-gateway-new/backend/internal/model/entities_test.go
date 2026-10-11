package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestRoleConstants(t *testing.T) {
	tests := []struct {
		name string
		role Role
		want string
	}{
		{name: "admin", role: RoleAdmin, want: "admin"},
		{name: "user", role: RoleUser, want: "user"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.role) != tc.want {
				t.Fatalf("role = %q, want %q", tc.role, tc.want)
			}
		})
	}

	t.Run("user json carries role verbatim", func(t *testing.T) {
		raw, err := json.Marshal(User{ID: 1, Role: RoleAdmin})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["role"] != "admin" {
			t.Fatalf("role json = %v, want admin", got["role"])
		}
	})
}

func TestProviderTypeConstants(t *testing.T) {
	tests := []struct {
		pt   ProviderType
		want string
	}{
		{ProviderOpenAICompat, "openai_compatible"},
		{ProviderAnthropic, "anthropic"},
		{ProviderGemini, "gemini"},
	}
	for _, tc := range tests {
		if string(tc.pt) != tc.want {
			t.Errorf("provider type = %q, want %q", tc.pt, tc.want)
		}
	}
}

func TestUserJSONHidesPasswordHash(t *testing.T) {
	u := User{
		ID:           7,
		Username:     "alice",
		DisplayName:  "Alice",
		Role:         RoleUser,
		PasswordHash: "super-secret-hash",
		Active:       true,
		CreatedAt:    time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["password_hash"]; ok {
		t.Fatalf("password_hash leaked: %s", raw)
	}
	if got["username"] != "alice" || got["display_name"] != "Alice" {
		t.Fatalf("identity fields wrong: %v", got)
	}
	if got["active"] != true {
		t.Fatalf("active = %v, want true", got["active"])
	}
	if got["created_at"] != "2024-03-01T12:00:00Z" {
		t.Fatalf("created_at = %v", got["created_at"])
	}
}

func TestAPIKeyJSONFields(t *testing.T) {
	key := APIKey{ID: 3, UserID: 1, Name: "prod", KeyPrefix: "sk-llm-", KeyHash: "hash", KeySuffix: "abcd", Active: true}
	raw, err := json.Marshal(key)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["key_hash"]; ok {
		t.Fatalf("key_hash leaked: %s", raw)
	}
	if got["key_prefix"] != "sk-llm-" || got["key_suffix"] != "abcd" {
		t.Fatalf("key display fields wrong: %v", got)
	}
	for _, omitted := range []string{"expires_at", "last_used_at", "model_allow"} {
		if _, ok := got[omitted]; ok {
			t.Errorf("%s should be omitted when empty: %s", omitted, raw)
		}
	}

	expiry := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	used := time.Date(2024, 6, 7, 8, 9, 10, 0, time.UTC)
	key.ExpiresAt = &expiry
	key.LastUsedAt = &used
	key.ModelAllow = []string{"gpt-4o", "claude"}
	raw, err = json.Marshal(key)
	if err != nil {
		t.Fatalf("marshal populated: %v", err)
	}
	var again APIKey
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatalf("unmarshal populated: %v", err)
	}
	if again.ExpiresAt == nil || !again.ExpiresAt.Equal(expiry) {
		t.Fatalf("ExpiresAt = %v, want %v", again.ExpiresAt, expiry)
	}
	if again.LastUsedAt == nil || !again.LastUsedAt.Equal(used) {
		t.Fatalf("LastUsedAt = %v, want %v", again.LastUsedAt, used)
	}
	if !reflect.DeepEqual(again.ModelAllow, []string{"gpt-4o", "claude"}) {
		t.Fatalf("ModelAllow = %v", again.ModelAllow)
	}
}

func TestProviderJSONHidesEncryptedKeyAndExtraHeaders(t *testing.T) {
	p := Provider{
		ID:           2,
		Name:         "openai",
		Type:         ProviderOpenAICompat,
		BaseURL:      "https://api.example.com",
		APIKeyEnc:    "ciphertext",
		TimeoutMS:    30000,
		Active:       true,
		HealthStatus: "available",
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["api_key_enc"]; ok {
		t.Fatalf("api_key_enc leaked: %s", raw)
	}
	if _, ok := got["extra_headers"]; ok {
		t.Fatalf("empty extra_headers should be omitted: %s", raw)
	}
	if got["type"] != "openai_compatible" || got["base_url"] != "https://api.example.com" {
		t.Fatalf("provider fields wrong: %v", got)
	}

	p.ExtraHeader = map[string]string{"x-tenant": "acme"}
	raw, _ = json.Marshal(p)
	var again Provider
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatalf("unmarshal with headers: %v", err)
	}
	if !reflect.DeepEqual(again.ExtraHeader, map[string]string{"x-tenant": "acme"}) {
		t.Fatalf("ExtraHeader = %v", again.ExtraHeader)
	}
	if again.MaxConcurrency != 0 {
		t.Fatalf("MaxConcurrency = %d, want 0", again.MaxConcurrency)
	}
}

func TestCapabilitiesRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		caps Capabilities
	}{
		{name: "all enabled", caps: Capabilities{Streaming: true, Multimodal: true, ToolCalling: true, StructuredOutput: true, Reasoning: true, ResponsesState: true}},
		{name: "none enabled", caps: Capabilities{}},
		{name: "mixed", caps: Capabilities{Streaming: true, Reasoning: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.caps)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Capabilities
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got != tc.caps {
				t.Fatalf("round trip = %+v, want %+v", got, tc.caps)
			}
		})
	}

	t.Run("explicit false is not omitted", func(t *testing.T) {
		raw, _ := json.Marshal(Capabilities{})
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, key := range []string{"streaming", "multimodal", "tool_calling", "structured_output", "reasoning", "responses_state"} {
			v, ok := got[key]
			if !ok || v != false {
				t.Errorf("capability %q = %v (present=%v), want false present", key, v, ok)
			}
		}
	})
}

func TestProviderModelNestedCapabilities(t *testing.T) {
	pm := ProviderModel{
		ID:           11,
		ProviderID:   2,
		ModelID:      "gpt-4o-mini",
		Capabilities: Capabilities{Streaming: true, ToolCalling: true},
		Active:       true,
		CreatedAt:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(pm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ProviderModel
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != pm.ID || got.ProviderID != pm.ProviderID || got.ModelID != pm.ModelID || got.Active != pm.Active {
		t.Fatalf("scalar fields lost: %+v", got)
	}
	if got.Capabilities != pm.Capabilities {
		t.Fatalf("capabilities = %+v, want %+v", got.Capabilities, pm.Capabilities)
	}
	if !got.CreatedAt.Equal(pm.CreatedAt) {
		t.Fatalf("CreatedAt = %v", got.CreatedAt)
	}
}

func TestModelAliasAndRouteEntry(t *testing.T) {
	alias := ModelAlias{ID: 5, Name: "fast", Active: true, CreatedAt: time.Date(2024, 2, 2, 0, 0, 0, 0, time.UTC)}
	raw, err := json.Marshal(alias)
	if err != nil {
		t.Fatalf("marshal alias: %v", err)
	}
	var gotAlias ModelAlias
	if err := json.Unmarshal(raw, &gotAlias); err != nil {
		t.Fatalf("unmarshal alias: %v", err)
	}
	if gotAlias != alias {
		t.Fatalf("alias round trip = %+v, want %+v", gotAlias, alias)
	}

	route := RouteEntry{ID: 9, AliasID: 5, ProviderID: 2, ProviderModel: "gpt-4o", Priority: 1, Active: true}
	raw, err = json.Marshal(route)
	if err != nil {
		t.Fatalf("marshal route: %v", err)
	}
	var gotRoute RouteEntry
	if err := json.Unmarshal(raw, &gotRoute); err != nil {
		t.Fatalf("unmarshal route: %v", err)
	}
	if gotRoute != route {
		t.Fatalf("route round trip = %+v, want %+v", gotRoute, route)
	}
}

func TestPriceVersionMoneyMapsRoundTrip(t *testing.T) {
	pv := PriceVersion{
		ID:      1,
		ModelID: "gpt-4o",
		Version: 3,
		Cost:    map[string]Money{"input": MustMoney("0.0015"), "output": MustMoney("0.006")},
		Credits: map[string]Money{"input": MustMoney("0.003"), "output": MustMoney("0.012")},
	}
	raw, err := json.Marshal(pv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PriceVersion
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Cost) != 2 || len(got.Credits) != 2 {
		t.Fatalf("map sizes = %d/%d, want 2/2", len(got.Cost), len(got.Credits))
	}
	if !got.Cost["input"].Equals(pv.Cost["input"]) || !got.Credits["output"].Equals(pv.Credits["output"]) {
		t.Fatalf("money values lost precision: %+v", got)
	}

	t.Run("nil maps marshal to null", func(t *testing.T) {
		raw, err := json.Marshal(PriceVersion{})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(got["cost"]) != "null" || string(got["credits"]) != "null" {
			t.Fatalf("nil money maps = %s / %s, want null", got["cost"], got["credits"])
		}
	})
}

func TestBudgetRuleNullableLimits(t *testing.T) {
	limit := MustMoney("100.50")
	r := BudgetRule{ID: 1, UserID: 2, WindowSecs: 3600, CostLimit: &limit, CreditLimit: &limit, Active: true}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got BudgetRule
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.CostLimit == nil || !got.CostLimit.Equals(limit) {
		t.Fatalf("CostLimit = %v, want %s", got.CostLimit, limit)
	}
	if got.CreditLimit == nil || !got.CreditLimit.Equals(limit) {
		t.Fatalf("CreditLimit = %v, want %s", got.CreditLimit, limit)
	}
	if got.WindowSecs != 3600 {
		t.Fatalf("WindowSecs = %d, want 3600", got.WindowSecs)
	}

	t.Run("nil limits omitted", func(t *testing.T) {
		raw, err := json.Marshal(BudgetRule{})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := keys["cost_limit"]; ok {
			t.Fatalf("cost_limit should be omitted: %s", raw)
		}
		if _, ok := keys["credit_limit"]; ok {
			t.Fatalf("credit_limit should be omitted: %s", raw)
		}
	})
}

func TestRateLimitRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   RateLimit
	}{
		{name: "user scope all models", in: RateLimit{ID: 1, Scope: "user", ScopeID: 4, RPM: 60, TPM: 100000, Concurrency: 5, Active: true}},
		{name: "model scope", in: RateLimit{ID: 2, Scope: "model", ScopeID: 4, ModelID: "gpt-4o", RPM: 0, TPM: 0, Concurrency: 0, Active: false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got RateLimit
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got != tc.in {
				t.Fatalf("round trip = %+v, want %+v", got, tc.in)
			}
		})
	}
}
