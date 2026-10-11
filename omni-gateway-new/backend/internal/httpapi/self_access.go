package httpapi

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

type accessModel struct {
	ID            string          `json:"id"`
	Protocols     []string        `json:"protocols"`
	Capabilities  map[string]bool `json:"capabilities"`
	PricingStatus string          `json:"pricing_status"`
	Ready         bool            `json:"ready"`
	Reasons       []string        `json:"reasons"`
}

func (h *selfHandler) access(w http.ResponseWriter, r *http.Request, user *model.User) {
	if !readOnly(r) {
		writeError(w, 405, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	id, err := parseQueryID(r, "api_key_id")
	if err != nil {
		writeError(w, 400, "invalid_request_error", "invalid_id", "api_key_id is required")
		return
	}
	keys, ok := h.store.(selfKeyStore)
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	key, ok := h.ownedAPIKey(w, r, keys, user, id)
	if !ok {
		return
	}
	routes, ok := h.store.(readinessStore)
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	successes, ok := h.store.(interface {
		FirstSuccessfulRequestAt(context.Context, int64, int64) (*time.Time, error)
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	first, err := successes.FirstSuccessfulRequestAt(r.Context(), user.ID, key.ID)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	aliases, err := routes.ListAliases(r.Context())
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	status := "active"
	if !key.Active {
		status = "inactive"
	} else if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now().UTC()) {
		status = "expired"
	}
	models := []accessModel{}
	for _, a := range aliases {
		if a == nil || !a.Active || !ModelAllowed(key, a.Name) {
			continue
		}
		ready, err := computeReadiness(r.Context(), routes, a)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		reasons := append([]string{}, ready.Reasons...)
		if status != "active" {
			reasons = appendUnique(reasons, "key_"+status)
		}
		models = append(models, accessModel{a.Name, ready.Protocols, ready.capabilities, ready.PricingStatus, ready.Ready && status == "active", reasons})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	writeJSON(w, 200, map[string]any{"api_key_id": id, "key_status": status, "first_success_at": first, "models": models})
}

// Never forward raw upstream text, routing topology, caller metadata or tags to
// the personal console. Diagnostics are a fixed safe Chinese vocabulary.
func safeOwnRequests(items []*model.Request) []*model.Request {
	out := make([]*model.Request, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		copy := *item
		copy.ClientIP = ""
		copy.UserAgent = ""
		copy.Tags = nil
		copy.ErrorSummary = ""
		copy.ErrorSuggestion = ""
		if copy.SettlementStatus == "" {
			copy.SettlementStatus = "none"
		}
		if copy.Status == model.StatusError {
			switch copy.ErrorType {
			case "rate_limit":
				copy.ErrorSummary = "请求触发限流"
				copy.ErrorSuggestion = "降低请求频率或并发，稍后重试。"
			case "model_not_found", "model":
				copy.ErrorSummary = "模型不可用"
				copy.ErrorSuggestion = "检查密钥白名单和有效模型列表。"
			case "model_not_allowed", "permission":
				copy.ErrorSummary = "密钥无此模型权限"
				copy.ErrorSuggestion = "检查本人密钥的模型范围。"
			case "timeout":
				copy.ErrorSummary = "上游响应超时"
				copy.ErrorSuggestion = "稍后重试；持续失败时向管理员提供请求 ID。"
			case "invalid_request", "invalid_request_error":
				copy.ErrorSummary = "请求参数不符合协议"
				copy.ErrorSuggestion = "检查模型、参数和所选协议。"
			case "authentication", "authentication_error":
				copy.ErrorSummary = "认证未通过"
				copy.ErrorSuggestion = "检查密钥启用状态和到期时间。"
			default:
				copy.ErrorSummary = "调用失败"
				copy.ErrorSuggestion = "检查接入配置；向管理员提供请求 ID 排查。"
			}
			switch copy.ErrorType {
			case "rate_limit", "model_not_found", "model_not_allowed", "timeout", "invalid_request", "invalid_request_error", "authentication", "authentication_error", "upstream", "routing", "model", "permission":
			default:
				copy.ErrorType = "gateway_error"
			}
		}
		out = append(out, &copy)
	}
	return out
}
