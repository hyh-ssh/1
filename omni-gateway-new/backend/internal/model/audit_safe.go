package model

import "encoding/json"

// SafeAuditSummary is a defence-in-depth whitelist shared by capture and
// persistence. Only configuration labels, IDs, switches, ranges and numeric
// rates survive. Credential/body/header/hash fields cannot be added by accident.
func SafeAuditSummary(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var wire map[string]any
	if json.Unmarshal(encoded, &wire) != nil {
		return nil
	}
	out := map[string]any{}
	for key, value := range wire {
		switch key {
		case "id", "name", "username", "user_id", "role", "active", "expires_at", "model_allow", "type", "supports_responses", "timeout_ms", "max_concurrency", "alias_id", "provider_id", "provider_model", "priority", "model_id", "version", "alias_ids", "scope", "scope_id", "rpm", "tpm", "concurrency", "enabled", "display_name", "local_enabled", "route_ids":
			switch value.(type) {
			case nil, string, bool, float64:
				out[key] = value
			case []any:
				safe := true
				for _, item := range value.([]any) {
					switch item.(type) {
					case string, float64:
					default:
						safe = false
					}
				}
				if safe {
					out[key] = value
				}
			}
		case "cost", "credits":
			if rates, ok := value.(map[string]any); ok {
				safe := map[string]string{}
				for meter, value := range rates {
					rate, ok := value.(string)
					if !ok {
						continue
					}
					if n, err := NewMoney(rate); err == nil && !n.IsNegative() {
						safe[meter] = n.String()
					}
				}
				if len(safe) > 0 {
					out[key] = safe
				}
			}
		}
	}
	return out
}
