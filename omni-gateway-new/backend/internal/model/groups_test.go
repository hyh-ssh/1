package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestModelGroupRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   ModelGroup
	}{
		{
			name: "with aliases",
			in:   ModelGroup{ID: 1, Name: "default", Active: true, AliasIDs: []int64{10, 20}, CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		{
			name: "empty alias list stays non-null",
			in:   ModelGroup{ID: 2, Name: "empty", AliasIDs: []int64{}},
		},
		{
			name: "nil alias list",
			in:   ModelGroup{ID: 3, Name: "nil"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got ModelGroup
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.ID != tc.in.ID || got.Name != tc.in.Name || got.Active != tc.in.Active {
				t.Fatalf("scalars = %+v, want %+v", got, tc.in)
			}
			if len(got.AliasIDs) != len(tc.in.AliasIDs) {
				t.Fatalf("AliasIDs = %v, want %v", got.AliasIDs, tc.in.AliasIDs)
			}
			for i := range tc.in.AliasIDs {
				if got.AliasIDs[i] != tc.in.AliasIDs[i] {
					t.Fatalf("AliasIDs[%d] = %d, want %d", i, got.AliasIDs[i], tc.in.AliasIDs[i])
				}
			}
			if !got.CreatedAt.Equal(tc.in.CreatedAt) || !got.UpdatedAt.Equal(tc.in.UpdatedAt) {
				t.Fatalf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, tc.in.CreatedAt, tc.in.UpdatedAt)
			}
		})
	}

	t.Run("empty slice vs nil json shape", func(t *testing.T) {
		emptyRaw, _ := json.Marshal(ModelGroup{AliasIDs: []int64{}})
		var emptyKeys map[string]json.RawMessage
		if err := json.Unmarshal(emptyRaw, &emptyKeys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(emptyKeys["alias_ids"]) != "[]" {
			t.Fatalf("empty AliasIDs json = %s, want []", emptyKeys["alias_ids"])
		}

		nilRaw, _ := json.Marshal(ModelGroup{})
		var nilKeys map[string]json.RawMessage
		if err := json.Unmarshal(nilRaw, &nilKeys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(nilKeys["alias_ids"]) != "null" {
			t.Fatalf("nil AliasIDs json = %s, want null", nilKeys["alias_ids"])
		}
	})
}

func TestUserGroupRoundTrip(t *testing.T) {
	ug := UserGroup{
		ID:            4,
		Name:          "team-a",
		Active:        true,
		UserIDs:       []int64{1, 2, 3},
		ModelGroupIDs: []int64{7},
		CreatedAt:     time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC),
		UpdatedAt:     time.Date(2024, 5, 7, 7, 8, 9, 0, time.UTC),
	}
	raw, err := json.Marshal(ug)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got UserGroup
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != ug.ID || got.Name != ug.Name || got.Active != ug.Active {
		t.Fatalf("scalars = %+v, want %+v", got, ug)
	}
	if !reflect.DeepEqual(got.UserIDs, ug.UserIDs) {
		t.Fatalf("UserIDs = %v, want %v", got.UserIDs, ug.UserIDs)
	}
	if !reflect.DeepEqual(got.ModelGroupIDs, ug.ModelGroupIDs) {
		t.Fatalf("ModelGroupIDs = %v, want %v", got.ModelGroupIDs, ug.ModelGroupIDs)
	}
	if !got.CreatedAt.Equal(ug.CreatedAt) || !got.UpdatedAt.Equal(ug.UpdatedAt) {
		t.Fatalf("timestamps lost")
	}
}

func TestModelAccessRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   ModelAccess
	}{
		{name: "restricted with models", in: ModelAccess{Restricted: true, Models: []string{"a", "b"}}},
		{name: "restricted without models", in: ModelAccess{Restricted: true, Models: []string{}}},
		{name: "unrestricted legacy policy", in: ModelAccess{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got ModelAccess
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Restricted != tc.in.Restricted {
				t.Fatalf("Restricted = %v, want %v", got.Restricted, tc.in.Restricted)
			}
			if !reflect.DeepEqual(got.Models, tc.in.Models) {
				t.Fatalf("Models = %v, want %v", got.Models, tc.in.Models)
			}
		})
	}

	t.Run("unrestricted encodes restricted false", func(t *testing.T) {
		raw, _ := json.Marshal(ModelAccess{})
		if string(raw) != `{"restricted":false,"models":null}` {
			t.Fatalf("ModelAccess json = %s", raw)
		}
	})
}
