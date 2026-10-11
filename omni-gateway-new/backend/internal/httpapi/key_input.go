package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// Track field presence, including explicit null, to distinguish clearing an
// owner's expiry from a non-owner attempting to edit it.
type apiKeyInput struct {
	UserID     *int64     `json:"user_id"`
	Name       string     `json:"name"`
	ExpiresAt  *time.Time `json:"expires_at"`
	ModelAllow []string   `json:"model_allow"`
	Active     *bool      `json:"active"`
	fields     map[string]json.RawMessage
}

func (in *apiKeyInput) UnmarshalJSON(data []byte) error {
	type wire apiKeyInput
	var v wire
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*in = apiKeyInput(v)
	if err := json.Unmarshal(data, &in.fields); err != nil {
		return err
	}
	canonical := make(map[string]json.RawMessage, len(in.fields))
	for name, value := range in.fields {
		canonical[strings.ToLower(name)] = value
	}
	in.fields = canonical
	return nil
}
func keyOwnerInput(w http.ResponseWriter, in apiKeyInput, owner int64, creating bool) bool {
	if _, present := in.fields["user_id"]; present && (in.UserID == nil || *in.UserID != owner) {
		code, status, message := "key_owner_immutable", 400, "API key ownership cannot be changed"
		if creating {
			code, status, message = "key_owner_required", 403, "API keys may only be issued for the session owner"
		}
		writeError(w, status, "permission_error", code, message)
		return false
	}
	return true
}
func applyOwnKeyInput(w http.ResponseWriter, in apiKeyInput, key *model.APIKey) bool {
	if !keyOwnerInput(w, in, key.UserID, false) {
		return false
	}
	if _, present := in.fields["name"]; present {
		name, ok := selfAPIKeyName(w, in.Name)
		if !ok {
			return false
		}
		key.Name = name
	}
	if !futureExpiry(w, in.ExpiresAt) {
		return false
	}
	if _, present := in.fields["expires_at"]; present {
		key.ExpiresAt = in.ExpiresAt
	}
	if _, present := in.fields["model_allow"]; present {
		key.ModelAllow = in.ModelAllow
	}
	if in.Active != nil {
		key.Active = *in.Active
	}
	return true
}
