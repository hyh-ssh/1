package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

type readinessStore interface {
	ListAliases(context.Context) ([]*model.ModelAlias, error)
	ListRouteEntries(context.Context, int64) ([]*model.RouteEntry, error)
	GetProvider(context.Context, int64) (*model.Provider, error)
}
type routeReadiness struct {
	ID             int64      `json:"id"`
	ProviderID     int64      `json:"provider_id"`
	ProviderName   string     `json:"provider_name"`
	ProviderModel  string     `json:"provider_model"`
	Active         bool       `json:"active"`
	ProviderActive bool       `json:"provider_active"`
	HealthStatus   string     `json:"health_status"`
	LastVerifiedAt *time.Time `json:"last_verified_at"`
	Protocols      []string   `json:"protocols"`
	PricingStatus  string     `json:"pricing_status"`
	PriceModelID   string     `json:"price_model_id"`
	PriceVersion   int        `json:"price_version"`
}
type aliasReadiness struct {
	Ready           bool             `json:"ready"`
	EffectiveRoutes int              `json:"effective_routes"`
	Protocols       []string         `json:"protocols"`
	PricingStatus   string           `json:"pricing_status"`
	Reasons         []string         `json:"reasons"`
	Routes          []routeReadiness `json:"routes"`
	capabilities    map[string]bool
}

func aliasByID(ctx context.Context, s interface {
	ListAliases(context.Context) ([]*model.ModelAlias, error)
}, id int64) (*model.ModelAlias, error) {
	aliases, err := s.ListAliases(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range aliases {
		if a != nil && a.ID == id {
			return a, nil
		}
	}
	return nil, store.ErrNotFound
}

func computeReadiness(ctx context.Context, s readinessStore, a *model.ModelAlias) (aliasReadiness, error) {
	out := aliasReadiness{Protocols: []string{}, PricingStatus: "unpriced", Reasons: []string{}, Routes: []routeReadiness{}}
	entries, err := s.ListRouteEntries(ctx, a.ID)
	if err != nil {
		return out, err
	}
	protocols := map[string]bool{}
	pricing := map[string]bool{}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		route := routeReadiness{ID: entry.ID, ProviderID: entry.ProviderID, ProviderModel: entry.ProviderModel, Active: entry.Active, Protocols: []string{}, PricingStatus: "unpriced"}
		p, err := s.GetProvider(ctx, entry.ProviderID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return out, err
		}
		if p != nil && err == nil {
			route.ProviderName = p.Name
			route.ProviderActive = p.Active
			route.HealthStatus = p.HealthStatus
			route.LastVerifiedAt = p.LastVerifiedAt
			route.Protocols = confirmedProtocols(p, nil)
			if models, ok := s.(interface {
				ListProviderModels(context.Context, int64) ([]*model.ProviderModel, error)
			}); ok {
				items, err := models.ListProviderModels(ctx, p.ID)
				if err != nil {
					return out, err
				}
				for _, pm := range items {
					if pm != nil && pm.Active && pm.ModelID == entry.ProviderModel {
						route.Protocols = confirmedProtocols(p, &pm.Capabilities)
						if entry.Active && p.Active && pm.Capabilities != (model.Capabilities{}) {
							out.capabilities = mergeCapabilities(out.capabilities, pm.Capabilities)
						}
						break
					}
				}
			}
		}
		if prices, ok := s.(interface {
			GetLatestPriceVersion(context.Context, string) (*model.PriceVersion, error)
		}); ok {
			price, err := prices.GetLatestPriceVersion(ctx, entry.ProviderModel)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return out, err
			}
			if price == nil || errors.Is(err, store.ErrNotFound) {
				price, err = prices.GetLatestPriceVersion(ctx, a.Name)
			}
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return out, err
			}
			if price != nil && err == nil {
				route.PricingStatus = model.PriceStatus(price)
				route.PriceModelID = price.ModelID
				route.PriceVersion = price.Version
			}
		}
		if entry.Active && route.ProviderActive {
			out.EffectiveRoutes++
			for _, p := range route.Protocols {
				protocols[p] = true
			}
			pricing[route.PricingStatus] = true
		}
		if !entry.Active {
			out.Reasons = appendUnique(out.Reasons, "route_inactive")
		} else if p == nil {
			out.Reasons = appendUnique(out.Reasons, "provider_missing")
		} else if !p.Active {
			out.Reasons = appendUnique(out.Reasons, "provider_inactive")
		}
		if route.PricingStatus == "unpriced" {
			out.Reasons = appendUnique(out.Reasons, "pricing_missing")
		}
		out.Routes = append(out.Routes, route)
	}
	for p := range protocols {
		out.Protocols = append(out.Protocols, p)
	}
	sort.Strings(out.Protocols)
	if len(pricing) == 1 {
		for p := range pricing {
			out.PricingStatus = p
		}
	} else if len(pricing) > 1 {
		out.PricingStatus = "mixed"
	}
	if !a.Active {
		out.Reasons = appendUnique(out.Reasons, "alias_inactive")
	}
	if out.EffectiveRoutes == 0 {
		out.Reasons = appendUnique(out.Reasons, "no_effective_route")
	}
	if out.capabilities == nil {
		out.Reasons = appendUnique(out.Reasons, "capabilities_unknown")
	}
	out.Ready = a.Active && out.EffectiveRoutes > 0
	return out, nil
}

// The gateway can convert chat/messages for all three adapters. Native
// Responses is explicit opt-in; embedding/image support needs model metadata,
// and is never guessed from the provider's URL or upstream model name.
func confirmedProtocols(p *model.Provider, c *model.Capabilities) []string {
	out := []string{}
	switch p.Type {
	case model.ProviderOpenAICompat, model.ProviderAnthropic, model.ProviderGemini:
		out = append(out, "chat_completions", "messages")
	}
	if p.Type == model.ProviderOpenAICompat {
		if p.SupportsResponses {
			out = append(out, "responses")
		}
		if c != nil && c.Embeddings {
			out = append(out, "embeddings")
		}
		if c != nil && c.ImagesGenerations {
			out = append(out, "images_generations")
		}
	}
	sort.Strings(out)
	return out
}
func mergeCapabilities(out map[string]bool, c model.Capabilities) map[string]bool {
	if out == nil {
		out = map[string]bool{}
	}
	for key, v := range map[string]bool{"streaming": c.Streaming, "multimodal": c.Multimodal, "tool_calling": c.ToolCalling, "structured_output": c.StructuredOutput, "reasoning": c.Reasoning, "responses_state": c.ResponsesState, "embeddings": c.Embeddings, "images_generations": c.ImagesGenerations} {
		out[key] = out[key] || v
	}
	return out
}
func appendUnique(items []string, value string) []string {
	for _, v := range items {
		if v == value {
			return items
		}
	}
	return append(items, value)
}

func (h *adminHandler) readiness(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != "GET" {
		notFound(w)
		return
	}
	s, ok := h.store.(readinessStore)
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	a, err := aliasByID(r.Context(), s, id)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	out, err := computeReadiness(r.Context(), s, a)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

type impactAlias struct {
	AliasID         int64  `json:"alias_id"`
	AliasName       string `json:"alias_name"`
	Active          bool   `json:"active"`
	RemainingRoutes int    `json:"remaining_routes"`
	LosesLastRoute  bool   `json:"loses_last_route"`
}

func (h *adminHandler) impact(w http.ResponseWriter, r *http.Request, resource string, id, routeID int64) {
	if r.Method != "GET" {
		notFound(w)
		return
	}
	s, ok := h.store.(readinessStore)
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	aliases, err := s.ListAliases(r.Context())
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	if resource == "provider" {
		if _, err := s.GetProvider(r.Context(), id); err != nil {
			writeAdminStoreError(w, err)
			return
		}
	} else {
		if _, err := aliasByID(r.Context(), s, id); err != nil {
			writeAdminStoreError(w, err)
			return
		}
	}
	affected := []impactAlias{}
	highRisk := false
	foundRoute := false
	for _, a := range aliases {
		if a == nil || (resource != "provider" && a.ID != id) {
			continue
		}
		entries, err := s.ListRouteEntries(r.Context(), a.ID)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		touches := resource == "alias"
		removed, remaining := 0, 0
		for _, entry := range entries {
			if entry == nil {
				continue
			}
			target := resource == "alias" || (resource == "provider" && entry.ProviderID == id) || (resource == "route" && entry.ID == routeID)
			if target {
				touches = true
				if resource == "route" {
					foundRoute = true
				}
			}
			if !entry.Active {
				continue
			}
			p, err := s.GetProvider(r.Context(), entry.ProviderID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				writeAdminStoreError(w, err)
				return
			}
			if p == nil || !p.Active {
				continue
			}
			if target {
				removed++
			} else {
				remaining++
			}
		}
		if touches {
			loses := a.Active && removed > 0 && remaining == 0
			highRisk = highRisk || loses
			affected = append(affected, impactAlias{a.ID, a.Name, a.Active, remaining, loses})
		}
	}
	if resource == "route" && !foundRoute {
		notFound(w)
		return
	}
	sort.Slice(affected, func(i, j int) bool { return affected[i].AliasID < affected[j].AliasID })
	writeJSON(w, 200, map[string]any{"resource": resource, "action": "disable_or_delete", "affected": affected, "high_risk": highRisk})
}
