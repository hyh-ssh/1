package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

type groupTestBackend struct {
	*fakeAuthStore
	models map[int64]*model.ModelGroup
	users  map[int64]*model.UserGroup
	err    error
}

func newGroupTestBackend() *groupTestBackend {
	auth := newFakeAuthStore(&model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, Active: true})
	auth.sessions["test-session"] = fakeSession{userID: 1, csrf: "csrf", expires: time.Now().Add(time.Hour)}
	return &groupTestBackend{fakeAuthStore: auth, models: map[int64]*model.ModelGroup{}, users: map[int64]*model.UserGroup{}}
}
func (s *groupTestBackend) ListModelGroups(context.Context) ([]*model.ModelGroup, error) {
	out := []*model.ModelGroup{}
	for _, g := range s.models {
		out = append(out, g)
	}
	return out, s.err
}
func (s *groupTestBackend) GetModelGroup(_ context.Context, id int64) (*model.ModelGroup, error) {
	if s.err != nil {
		return nil, s.err
	}
	g := s.models[id]
	if g == nil {
		return nil, store.ErrNotFound
	}
	copy := *g
	return &copy, nil
}
func (s *groupTestBackend) CreateModelGroup(_ context.Context, g *model.ModelGroup) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	g.ID = int64(len(s.models) + 1)
	s.models[g.ID] = g
	return g.ID, nil
}
func (s *groupTestBackend) UpdateModelGroup(_ context.Context, g *model.ModelGroup) error {
	if s.err != nil {
		return s.err
	}
	s.models[g.ID] = g
	return nil
}
func (s *groupTestBackend) DeleteModelGroup(_ context.Context, id int64) error {
	if s.err != nil {
		return s.err
	}
	delete(s.models, id)
	return nil
}
func (s *groupTestBackend) ListUserGroups(context.Context) ([]*model.UserGroup, error) {
	out := []*model.UserGroup{}
	for _, g := range s.users {
		out = append(out, g)
	}
	return out, s.err
}
func (s *groupTestBackend) GetUserGroup(_ context.Context, id int64) (*model.UserGroup, error) {
	if s.err != nil {
		return nil, s.err
	}
	g := s.users[id]
	if g == nil {
		return nil, store.ErrNotFound
	}
	copy := *g
	return &copy, nil
}
func (s *groupTestBackend) CreateUserGroup(_ context.Context, g *model.UserGroup) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	g.ID = int64(len(s.users) + 1)
	s.users[g.ID] = g
	return g.ID, nil
}
func (s *groupTestBackend) UpdateUserGroup(_ context.Context, g *model.UserGroup) error {
	if s.err != nil {
		return s.err
	}
	s.users[g.ID] = g
	return nil
}
func (s *groupTestBackend) DeleteUserGroup(_ context.Context, id int64) error {
	if s.err != nil {
		return s.err
	}
	delete(s.users, id)
	return nil
}
func (s *groupTestBackend) GetUserModelAccess(context.Context, int64) (*model.ModelAccess, error) {
	return &model.ModelAccess{}, s.err
}

func groupRequest(handler http.Handler, method, path, body string, session, csrf bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if session {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
	}
	if csrf {
		r.Header.Set("X-CSRF-Token", "csrf")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestModelGroupAdminClassificationCRUD(t *testing.T) {
	s := newGroupTestBackend()
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	path := "/api/admin/model-groups"
	w := groupRequest(h, "POST", path, `{"name":" Team ","alias_ids":[3,4]}`, true, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var created model.ModelGroup
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Name != "Team" || !created.Active {
		t.Fatal(w.Body)
	}
	for _, suffix := range []string{"", "/1"} {
		if w := groupRequest(h, "GET", path+suffix, "", true, false); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := groupRequest(h, "PATCH", path+"/1", `{"active":false}`, true, true); w.Code != 200 || len(s.models[1].AliasIDs) != 2 {
		t.Fatal(w.Code, w.Body)
	}
	if w := groupRequest(h, "PATCH", path+"/1", `{"name":"Renamed","alias_ids":[]}`, true, true); w.Code != 200 || len(s.models[1].AliasIDs) != 0 || s.models[1].Name != "Renamed" {
		t.Fatal(w.Code, w.Body)
	}
	if w := groupRequest(h, "DELETE", path+"/1", "", true, true); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w := groupRequest(h, "GET", path+"/1", "", true, false); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestModelGroupAdminAuthorizationAndValidation(t *testing.T) {
	s := newGroupTestBackend()
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	for _, tc := range []struct {
		method, path, body string
		session, csrf      bool
		status             int
	}{
		{"GET", "/api/admin/model-groups", "", false, false, 401},
		{"POST", "/api/admin/model-groups", `{"name":"team"}`, true, false, 403},
		{"POST", "/api/admin/model-groups", `{}`, true, true, 400},
		{"POST", "/api/admin/model-groups", `{"name":" "}`, true, true, 400},
		{"POST", "/api/admin/model-groups", `{"name":"team","alias_ids":[0]}`, true, true, 400},
		{"POST", "/api/admin/model-groups", `{"name":"team","alias_ids":[1,1]}`, true, true, 400},
		{"POST", "/api/admin/model-groups", `{"name":"team","alias_ids":[-1]}`, true, true, 400},
		{"POST", "/api/admin/model-groups", `{"name":"team"} {}`, true, true, 400},
		{"GET", "/api/admin/model-groups/nope", "", true, false, 400},
		{"GET", "/api/admin/model-groups/0", "", true, false, 400},
		{"GET", "/api/admin/model-groups/1/extra", "", true, false, 404},
		{"PUT", "/api/admin/model-groups", "", true, true, 405},
		{"OPTIONS", "/api/admin/model-groups", "", false, false, 204},
	} {
		if w := groupRequest(h, tc.method, tc.path, tc.body, tc.session, tc.csrf); w.Code != tc.status {
			t.Errorf("%s %s=%d want %d %s", tc.method, tc.path, w.Code, tc.status, w.Body)
		}
	}
	groupRequest(h, "POST", "/api/admin/model-groups", `{"name":"existing"}`, true, true)
	for _, body := range []string{`{"name":""}`, `{"name":" "}`, `{bad}`, `{"name":"` + strings.Repeat("x", 201) + `"}`} {
		if w := groupRequest(h, "PATCH", "/api/admin/model-groups/1", body, true, true); w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	s.usersByID[1].Role = model.RoleUser
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		if w := groupRequest(h, method, "/api/admin/model-groups/1", `{"name":"team"}`, true, true); w.Code != 403 || !strings.Contains(w.Body.String(), "administrator_required") {
			t.Fatal(w.Code, w.Body)
		}
	}
}

func TestRetiredUserGroupsUseNormalAdminGateAndPreserveRelations(t *testing.T) {
	s := newGroupTestBackend()
	s.users[1] = &model.UserGroup{ID: 1, Name: "historical", Active: true, UserIDs: []int64{1}, ModelGroupIDs: []int64{2}}
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		if w := groupRequest(h, method, "/api/admin/user-groups/1", `{"name":"changed"}`, false, false); w.Code != 401 {
			t.Fatal(w.Code, w.Body)
		}
		if w := groupRequest(h, method, "/api/admin/user-groups/1", `{"name":"changed"}`, true, true); w.Code != 404 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if len(s.users) != 1 || s.users[1].Name != "historical" || len(s.users[1].UserIDs) != 1 {
		t.Fatal("history mutated")
	}
	s.usersByID[1].Role = model.RoleUser
	if w := groupRequest(h, "GET", "/api/admin/user-groups", "", true, false); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestModelGroupAdminStoreFailuresAreSafe(t *testing.T) {
	s := newGroupTestBackend()
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	for _, tc := range []struct {
		err    error
		status int
	}{{store.ErrGroupConflict, 409}, {store.ErrInvalidGroup, 400}, {store.ErrNotFound, 404}, {errors.New("postgres password=secret"), 500}} {
		s.err = tc.err
		for _, method := range []string{"GET", "POST"} {
			if w := groupRequest(h, method, "/api/admin/model-groups", `{"name":"team"}`, true, true); w.Code != tc.status || strings.Contains(w.Body.String(), "password") {
				t.Fatal(w.Code, w.Body)
			}
		}
	}
	h = NewHandlerWithAuth(nil, s.fakeAuthStore, AuthConfig{})
	if w := groupRequest(h, "GET", "/api/admin/model-groups", "", true, false); w.Code != 501 {
		t.Fatal(w.Code, w.Body)
	}
	RegisterGroupRoutes(nil, s, AuthConfig{})
	RegisterGroupRoutes(http.NewServeMux(), nil, AuthConfig{})
}
