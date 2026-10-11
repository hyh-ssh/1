package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

type auditResponse struct {
	http.ResponseWriter
	status    int
	createdID string
}

func (w *auditResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *auditResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	// Extract only the newly created object's identity. Never retain the response
	// (which may contain a one-time secret), and never inspect content-log bodies.
	if w.status == 201 {
		var v struct {
			ID  int64 `json:"id"`
			Key struct {
				ID int64 `json:"id"`
			} `json:"key"`
		}
		if json.Unmarshal(body, &v) == nil {
			if v.ID == 0 {
				v.ID = v.Key.ID
			}
			if v.ID > 0 {
				w.createdID = strconv.FormatInt(v.ID, 10)
			}
		}
	}
	return w.ResponseWriter.Write(body)
}
func beginChangeAudit(w http.ResponseWriter, r *http.Request, s AuthStore, user *model.User, path, detail string) (http.ResponseWriter, func()) {
	out := &auditResponse{ResponseWriter: w}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	resource := parts[0]
	id := ""
	if len(parts) > 1 {
		id = parts[1]
	}
	before := model.SafeAuditSummary(auditSnapshot(r.Context(), s, parts))
	return out, func() {
		auditor, ok := s.(interface {
			InsertAudit(context.Context, *model.AuditEntry) error
		})
		if !ok {
			return
		}
		status := out.status
		if status == 0 {
			status = 200
		}
		result := "success"
		if status >= 400 {
			result = "failure"
		}
		if status == 401 || status == 403 {
			result = "denied"
		}
		if id == "" && out.createdID != "" {
			id = out.createdID
			parts = append(parts, id)
		}
		if len(parts) == 3 && parts[0] == "aliases" && parts[2] == "routes" && out.createdID != "" {
			parts = append(parts, out.createdID)
		}
		after := model.SafeAuditSummary(auditSnapshot(r.Context(), s, parts))
		name := ""
		if value, ok := before["name"].(string); ok {
			name = value
		}
		if value, ok := after["name"].(string); ok {
			name = value
		}
		if user == nil {
			return
		}
		entry := &model.AuditEntry{ActorID: user.ID, ActorName: user.Username, Action: r.Method, Resource: resource, ResourceID: id, ResourceName: name, Detail: detail, IP: requestClientIP(r), Result: result, Before: before, After: after}
		if len(parts) > 3 && parts[2] == "routes" {
			entry.Resource = "routes"
			entry.ResourceID = parts[3]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = auditor.InsertAudit(ctx, entry)
	}
}

// Each resource has an explicit safe projection. No reflection or request body
// merging: newly introduced credential fields can never enter a summary.
func auditSnapshot(ctx context.Context, s AuthStore, parts []string) map[string]any {
	if len(parts) == 0 {
		return nil
	}
	id := int64(0)
	if len(parts) > 1 {
		id, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	switch parts[0] {
	case "password":
		return nil
	case "api-keys":
		if id <= 0 {
			return nil
		}
		if q, ok := s.(interface {
			GetAPIKeyByID(context.Context, int64) (*model.APIKey, error)
		}); ok {
			v, e := q.GetAPIKeyByID(ctx, id)
			if e == nil && v != nil {
				return map[string]any{"id": v.ID, "user_id": v.UserID, "name": v.Name, "active": v.Active, "expires_at": v.ExpiresAt, "model_allow": append([]string{}, v.ModelAllow...)}
			}
		}
	case "providers":
		if id <= 0 {
			return nil
		}
		if q, ok := s.(interface {
			GetProvider(context.Context, int64) (*model.Provider, error)
		}); ok {
			v, e := q.GetProvider(ctx, id)
			if e == nil && v != nil {
				return map[string]any{"id": v.ID, "name": v.Name, "active": v.Active, "type": v.Type, "supports_responses": v.SupportsResponses, "timeout_ms": v.TimeoutMS, "max_concurrency": v.MaxConcurrency}
			}
		}
	case "aliases":
		if q, ok := s.(readinessStore); ok {
			if len(parts) == 3 && parts[2] == "routes" {
				return nil
			}
			if len(parts) == 4 && parts[2] == "routes" && parts[3] == "order" {
				items, e := q.ListRouteEntries(ctx, id)
				if e != nil {
					return nil
				}
				ids := []int64{}
				for _, v := range items {
					if v != nil {
						ids = append(ids, v.ID)
					}
				}
				return map[string]any{"id": id, "route_ids": ids}
			}
			if len(parts) > 3 && parts[2] == "routes" {
				routeID, _ := strconv.ParseInt(parts[3], 10, 64)
				items, e := q.ListRouteEntries(ctx, id)
				if e == nil {
					for _, v := range items {
						if v != nil && v.ID == routeID {
							return map[string]any{"id": v.ID, "alias_id": v.AliasID, "provider_id": v.ProviderID, "provider_model": v.ProviderModel, "active": v.Active, "priority": v.Priority}
						}
					}
				}
				return nil
			}
			v, e := aliasByID(ctx, q, id)
			if e == nil && v != nil {
				return map[string]any{"id": v.ID, "name": v.Name, "active": v.Active}
			}
		}
	case "users":
		if id <= 0 {
			return nil
		}
		v, e := s.GetUserByID(ctx, id)
		if e == nil && v != nil {
			return map[string]any{"id": v.ID, "name": v.Username, "role": v.Role, "active": v.Active, "local_enabled": v.PasswordLoginEnabled}
		}
	case "model-groups":
		if q, ok := s.(interface {
			GetModelGroup(context.Context, int64) (*model.ModelGroup, error)
		}); ok {
			v, e := q.GetModelGroup(ctx, id)
			if e == nil && v != nil {
				return map[string]any{"id": v.ID, "name": v.Name, "active": v.Active, "alias_ids": append([]int64{}, v.AliasIDs...)}
			}
		}
	case "prices":
		if id <= 0 {
			return nil
		}
		if q, ok := s.(interface {
			ListAllPriceVersions(context.Context) ([]*model.PriceVersion, error)
		}); ok {
			items, e := q.ListAllPriceVersions(ctx)
			if e == nil {
				for _, v := range items {
					if v != nil && v.ID == id {
						return map[string]any{"id": v.ID, "model_id": v.ModelID, "version": v.Version, "cost": v.Cost, "credits": v.Credits}
					}
				}
			}
		}
	case "oidc-provider":
		if q, ok := s.(oidcProviderAdminStore); ok {
			v, exists, e := q.GetOIDCProvider(ctx)
			if e == nil && exists {
				return map[string]any{"enabled": v.Enabled, "display_name": v.DisplayName}
			}
		}
	case "rate-limits":
		if q, ok := s.(interface {
			GetRateLimit(context.Context, int64) (*model.RateLimit, error)
		}); ok {
			v, e := q.GetRateLimit(ctx, id)
			if e == nil && v != nil {
				return map[string]any{"id": v.ID, "scope": v.Scope, "scope_id": v.ScopeID, "model_id": v.ModelID, "rpm": v.RPM, "tpm": v.TPM, "concurrency": v.Concurrency, "active": v.Active}
			}
		}
	}
	return nil
}
