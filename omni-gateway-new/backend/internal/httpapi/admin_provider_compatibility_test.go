package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func TestAdminProviderCompatibilityProfileCRUD(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	decode := func(method, path, body string, status int) model.Provider {
		t.Helper()
		response := h.expect(method, path, body, status)
		var provider model.Provider
		if err := json.Unmarshal(response.Body.Bytes(), &provider); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
		return provider
	}

	generic := decode(http.MethodPost, "/api/admin/providers", `{"name":"generic","type":"anthropic","base_url":"https://api.example.com"}`, http.StatusCreated)
	if generic.CompatibilityProfile != model.ProviderCompatibilityGeneric {
		t.Fatalf("omitted profile = %q, want generic", generic.CompatibilityProfile)
	}
	deepseek := decode(http.MethodPost, "/api/admin/providers", `{"name":"DeepSeek","type":"openai_compatible","compatibility_profile":"deepseek","base_url":"https://api.deepseek.com/v1"}`, http.StatusCreated)
	if deepseek.CompatibilityProfile != model.ProviderCompatibilityDeepSeek {
		t.Fatalf("created profile = %q, want deepseek", deepseek.CompatibilityProfile)
	}
	path := "/api/admin/providers/" + itoa(int(deepseek.ID))
	if got := decode(http.MethodGet, path, "", http.StatusOK); got.CompatibilityProfile != model.ProviderCompatibilityDeepSeek {
		t.Fatalf("GET profile = %q", got.CompatibilityProfile)
	}
	var listed []model.Provider
	if err := json.Unmarshal(h.expect(http.MethodGet, "/api/admin/providers", "", http.StatusOK).Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].CompatibilityProfile == "" || listed[1].CompatibilityProfile == "" {
		t.Fatalf("list profiles = %+v", listed)
	}
	if got := decode(http.MethodPatch, path, `{"active":false}`, http.StatusOK); got.CompatibilityProfile != model.ProviderCompatibilityDeepSeek {
		t.Fatalf("unrelated PATCH cleared profile: %q", got.CompatibilityProfile)
	}
	if got := decode(http.MethodPatch, path, `{"compatibility_profile":"generic","type":"anthropic"}`, http.StatusOK); got.CompatibilityProfile != model.ProviderCompatibilityGeneric || got.Type != model.ProviderAnthropic {
		t.Fatalf("patched provider = %+v", got)
	}
	if got := decode(http.MethodGet, path, "", http.StatusOK); got.CompatibilityProfile != model.ProviderCompatibilityGeneric {
		t.Fatalf("profile after patch = %q", got.CompatibilityProfile)
	}
}

func TestAdminProviderCompatibilityProfileRejectsUnsupportedCombinations(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	for _, body := range []string{
		`{"name":"wrong","type":"anthropic","compatibility_profile":"deepseek","base_url":"https://api.example.com"}`,
		`{"name":"wrong","type":"gemini","compatibility_profile":"deepseek","base_url":"https://api.example.com"}`,
		`{"name":"wrong","type":"openai_compatible","compatibility_profile":"unknown","base_url":"https://api.example.com"}`,
		`{"name":"wrong","type":"openai_compatible","compatibility_profile":"","base_url":"https://api.example.com"}`,
	} {
		response := h.expect(http.MethodPost, "/api/admin/providers", body, http.StatusBadRequest)
		if !strings.Contains(response.Body.String(), "invalid_provider") || !strings.Contains(response.Body.String(), "compatibility_profile") {
			t.Fatalf("invalid profile response = %s", response.Body.String())
		}
	}
	created := h.expect(http.MethodPost, "/api/admin/providers", `{"name":"DeepSeek","type":"openai_compatible","compatibility_profile":"deepseek","base_url":"https://api.example.com"}`, http.StatusCreated)
	var provider model.Provider
	if err := json.Unmarshal(created.Body.Bytes(), &provider); err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/providers/" + itoa(int(provider.ID))
	for _, body := range []string{
		`{"type":"anthropic"}`,
		`{"compatibility_profile":"unknown"}`,
		`{"compatibility_profile":""}`,
	} {
		response := h.expect(http.MethodPatch, path, body, http.StatusBadRequest)
		if !strings.Contains(response.Body.String(), "invalid_provider") || !strings.Contains(response.Body.String(), "compatibility_profile") {
			t.Fatalf("invalid patch response = %s", response.Body.String())
		}
	}
}
