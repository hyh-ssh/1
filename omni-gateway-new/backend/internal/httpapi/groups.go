package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// Only model-group classification is exposed. Historical user-group stores and
// relation tables remain intact, but no user-group HTTP handler is mounted.
type GroupStore interface{ store.ModelGroupStore }

func RegisterGroupRoutes(mux *http.ServeMux, authStore AuthStore, cfg AuthConfig) {
	if mux == nil || authStore == nil {
		return
	}
	h := newGroupHandler(authStore, cfg, "model")
	mux.Handle("/api/admin/model-groups", h)
	mux.Handle("/api/admin/model-groups/", h)
}

type groupHandler struct {
	auth  *authHandler
	store GroupStore
}

func newGroupHandler(authStore AuthStore, cfg AuthConfig, kind string) http.Handler {
	groups, _ := authStore.(GroupStore)
	return &groupHandler{auth: newAuthHandler(authStore, cfg), store: groups}
}
func (h *groupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(204)
		return
	}
	user, csrf, ok := h.auth.authenticate(r)
	if !ok {
		writeError(w, 401, "authentication_error", "authentication_required", "administrator authentication required")
		return
	}
	if !readOnly(r) {
		var finish func()
		w, finish = beginChangeAudit(w, r, h.auth.store, user, strings.TrimPrefix(r.URL.Path, "/api/admin/"), "admin API mutation")
		defer finish()
	}
	if user.Role != model.RoleAdmin {
		writeError(w, 403, "invalid_request_error", "administrator_required", "administrator privileges required")
		return
	}
	if !readOnly(r) && !sameToken(r.Header.Get("X-CSRF-Token"), csrf) {
		writeError(w, 403, "invalid_request_error", "csrf_failed", "invalid CSRF token")
		return
	}
	if h.store == nil {
		writeAdminUnsupported(w)
		return
	}
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/model-groups"), "/")
	if p == "" {
		h.listCreate(w, r)
		return
	}
	if strings.Contains(p, "/") {
		notFound(w)
		return
	}
	id, err := strconv.ParseInt(p, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "invalid_request_error", "invalid_id", "invalid group id")
		return
	}
	h.item(w, r, id)
}

type modelGroupInput struct {
	Name     *string `json:"name"`
	Active   *bool   `json:"active"`
	AliasIDs []int64 `json:"alias_ids"`
}

func groupActive(v *bool) bool { return v == nil || *v }
func (h *groupHandler) listCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		v, err := h.store.ListModelGroups(r.Context())
		if err != nil {
			writeGroupStoreError(w, err)
			return
		}
		if v == nil {
			v = []*model.ModelGroup{}
		}
		writeJSON(w, 200, v)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	var in modelGroupInput
	if decodeAdminJSON(w, r, &in) != nil || in.Name == nil || !validGroupInput(*in.Name, in.AliasIDs) {
		writeError(w, 400, "invalid_request_error", "invalid_request", "name and valid aliases are required")
		return
	}
	g := &model.ModelGroup{Name: strings.TrimSpace(*in.Name), Active: groupActive(in.Active), AliasIDs: groupIDs(in.AliasIDs)}
	id, err := h.store.CreateModelGroup(r.Context(), g)
	if err != nil {
		writeGroupStoreError(w, err)
		return
	}
	g.ID = id
	writeJSON(w, 201, g)
}
func (h *groupHandler) item(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != "GET" && r.Method != "PATCH" && r.Method != "DELETE" {
		writeError(w, 405, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	g, err := h.store.GetModelGroup(r.Context(), id)
	if err != nil {
		writeGroupStoreError(w, err)
		return
	}
	copy := *g
	g = &copy
	if r.Method == "GET" {
		writeJSON(w, 200, g)
		return
	}
	if r.Method == "DELETE" {
		if err := h.store.DeleteModelGroup(r.Context(), id); err != nil {
			writeGroupStoreError(w, err)
			return
		}
		w.WriteHeader(204)
		return
	}
	var in modelGroupInput
	if decodeAdminJSON(w, r, &in) != nil {
		writeError(w, 400, "invalid_request_error", "invalid_request", "invalid group request")
		return
	}
	if in.Name != nil {
		g.Name = strings.TrimSpace(*in.Name)
	}
	if in.Active != nil {
		g.Active = *in.Active
	}
	if in.AliasIDs != nil {
		g.AliasIDs = in.AliasIDs
	}
	if !validGroupInput(g.Name, g.AliasIDs) {
		writeGroupStoreError(w, store.ErrInvalidGroup)
		return
	}
	if err := h.store.UpdateModelGroup(r.Context(), g); err != nil {
		writeGroupStoreError(w, err)
		return
	}
	writeJSON(w, 200, g)
}
func writeGroupStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "invalid_request_error", "not_found", "resource not found")
	case errors.Is(err, store.ErrInvalidGroup):
		writeError(w, 400, "invalid_request_error", "invalid_group", "invalid group or relation")
	case errors.Is(err, store.ErrGroupConflict):
		writeError(w, 409, "invalid_request_error", "group_conflict", "group name already exists")
	default:
		writeAdminStoreError(w, err)
	}
}
func groupIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}
func validGroupInput(name string, relations ...[]int64) bool {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 || strings.ContainsRune(name, 0) {
		return false
	}
	for _, ids := range relations {
		if len(ids) > 10000 {
			return false
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			if id <= 0 || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	return true
}
