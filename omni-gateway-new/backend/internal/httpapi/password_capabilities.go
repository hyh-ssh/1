package httpapi

import (
	"context"
	"github.com/zjvtit/omni-gateway/internal/model"
)

type passwordCapabilities struct {
	LocalEnabled         bool `json:"local_enabled"`
	CanSetWithoutCurrent bool `json:"can_set_without_current"`
	CanRecover           bool `json:"can_recover"`
}

func authUserWithCapabilities(ctx context.Context, s AuthStore, user *model.User) authUser {
	out := toAuthUser(user)
	if !user.PasswordLoginEnabled {
		if bindings, ok := s.(interface {
			HasOIDCBinding(context.Context, int64) (bool, error)
		}); ok {
			bound, err := bindings.HasOIDCBinding(ctx, user.ID)
			out.PasswordCapabilities.CanSetWithoutCurrent = err == nil && bound
		}
	}
	return out
}
