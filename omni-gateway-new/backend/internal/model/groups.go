package model

import "time"

// ModelGroup grants access to a set of public model aliases.
type ModelGroup struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Active    bool      `json:"active"`
	AliasIDs  []int64   `json:"alias_ids"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserGroup assigns users to model groups.
type UserGroup struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Active        bool      `json:"active"`
	UserIDs       []int64   `json:"user_ids"`
	ModelGroupIDs []int64   `json:"model_group_ids"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ModelAccess is the union of active group grants for a user. Restricted is
// true whenever any group membership exists, including disabled groups.
// Users without memberships retain the legacy API-key-only access policy.
type ModelAccess struct {
	Restricted bool     `json:"restricted"`
	Models     []string `json:"models"`
}
