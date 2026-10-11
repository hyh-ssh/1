package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/zjvtit/omni-gateway/internal/model"
)

func groupTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL group integration tests")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.PingContext(ctx); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := fmt.Sprintf("groups_test_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
	})
	dsn, err := urlpkgWithSearchPath(url, schema)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func urlpkgWithSearchPath(raw, schema string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("options", "-csearch_path="+schema)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func TestHistoricalGroupsPostgresCRUDAndAccessProjection(t *testing.T) {
	s := groupTestStore(t)
	ctx := context.Background()
	userID, err := s.CreateUser(ctx, &model.User{Username: "alice", PasswordHash: "not-a-credential", Role: model.RoleUser, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := s.CreateUser(ctx, &model.User{Username: "bob", PasswordHash: "not-a-credential", Role: model.RoleUser, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := s.CreateAlias(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	vision, err := s.CreateAlias(ctx, "vision")
	if err != nil {
		t.Fatal(err)
	}
	// Seed pre-retirement active aliases as historical fixtures; new aliases are drafts.
	if _, err = s.pool.Exec(ctx, `UPDATE model_aliases SET active=TRUE WHERE id=$1 OR id=$2`, chat, vision); err != nil {
		t.Fatal(err)
	}
	assertAccess := func(user int64, restricted bool, names ...string) {
		t.Helper()
		got, err := s.GetUserModelAccess(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		if got.Restricted != restricted || strings.Join(got.Models, ",") != strings.Join(names, ",") {
			t.Fatalf("access=%+v, want restricted=%v models=%v", got, restricted, names)
		}
	}
	assertAccess(userID, false)
	mg := &model.ModelGroup{Name: "basic", Active: true, AliasIDs: []int64{chat}}
	mg.ID, err = s.CreateModelGroup(ctx, mg)
	if err != nil {
		t.Fatal(err)
	}
	premium := &model.ModelGroup{Name: "premium", Active: true, AliasIDs: []int64{chat, vision}}
	premium.ID, err = s.CreateModelGroup(ctx, premium)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := s.ListModelGroups(ctx)
	if err != nil || len(groups) != 2 {
		t.Fatalf("model groups=%v err=%v", groups, err)
	}
	got, err := s.GetModelGroup(ctx, mg.ID)
	if err != nil || !reflect.DeepEqual(got.AliasIDs, []int64{chat}) || got.CreatedAt.IsZero() {
		t.Fatalf("model group=%+v err=%v", got, err)
	}
	ug := &model.UserGroup{Name: "team", Active: true, UserIDs: []int64{userID}, ModelGroupIDs: []int64{mg.ID}}
	ug.ID, err = s.CreateUserGroup(ctx, ug)
	if err != nil {
		t.Fatal(err)
	}
	second := &model.UserGroup{Name: "second", Active: true, UserIDs: []int64{userID}, ModelGroupIDs: []int64{premium.ID}}
	second.ID, err = s.CreateUserGroup(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	users, err := s.ListUserGroups(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("user groups=%v err=%v", users, err)
	}
	fetched, err := s.GetUserGroup(ctx, ug.ID)
	if err != nil || !reflect.DeepEqual(fetched.UserIDs, ug.UserIDs) || !reflect.DeepEqual(fetched.ModelGroupIDs, ug.ModelGroupIDs) {
		t.Fatalf("user group=%+v err=%v", fetched, err)
	}
	assertAccess(userID, true, "chat", "vision")
	assertAccess(otherID, false)
	second.Active = false
	if err = s.UpdateUserGroup(ctx, second); err != nil {
		t.Fatal(err)
	}
	assertAccess(userID, true, "chat")
	mg.Active = false
	if err = s.UpdateModelGroup(ctx, mg); err != nil {
		t.Fatal(err)
	}
	assertAccess(userID, true)
	mg.Active = true
	if err = s.UpdateModelGroup(ctx, mg); err != nil {
		t.Fatal(err)
	}
	if err = s.SetAliasActive(ctx, chat, false); err != nil {
		t.Fatal(err)
	}
	assertAccess(userID, true)
	if _, err = s.pool.Exec(ctx, `UPDATE model_aliases SET active=TRUE WHERE id=$1`, chat); err != nil {
		t.Fatal(err)
	}
	mg.AliasIDs = []int64{vision}
	if err = s.UpdateModelGroup(ctx, mg); err != nil {
		t.Fatal(err)
	}
	assertAccess(userID, true, "vision")

	// Invalid full replacements must roll back both scalar and relation writes.
	mg.Name = "should-roll-back"
	mg.AliasIDs = []int64{99999999}
	if err = s.UpdateModelGroup(ctx, mg); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("invalid model relation err=%v", err)
	}
	got, err = s.GetModelGroup(ctx, mg.ID)
	if err != nil || got.Name != "basic" || !reflect.DeepEqual(got.AliasIDs, []int64{vision}) {
		t.Fatalf("model rollback=%+v err=%v", got, err)
	}
	ug.Name = "should-roll-back"
	ug.UserIDs = []int64{otherID}
	ug.ModelGroupIDs = []int64{99999999}
	if err = s.UpdateUserGroup(ctx, ug); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("invalid user relation err=%v", err)
	}
	fetched, err = s.GetUserGroup(ctx, ug.ID)
	if err != nil || fetched.Name != "team" || !reflect.DeepEqual(fetched.UserIDs, []int64{userID}) {
		t.Fatalf("user rollback=%+v err=%v", fetched, err)
	}
	assertAccess(userID, true, "vision")
	assertAccess(otherID, false)
	if _, err = s.CreateModelGroup(ctx, &model.ModelGroup{Name: "basic", Active: true}); !errors.Is(err, ErrGroupConflict) {
		t.Fatalf("duplicate group err=%v", err)
	}
	if _, err = s.CreateUserGroup(ctx, &model.UserGroup{Name: "bad-create", UserIDs: []int64{99999999}}); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("invalid create err=%v", err)
	}
	users, err = s.ListUserGroups(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("failed create leaked group: %v %v", users, err)
	}
	if err = s.DeleteModelGroup(ctx, mg.ID); err != nil {
		t.Fatal(err)
	}
	assertAccess(userID, true)
	// Disabled group memberships must never restore unrestricted access.
	if err = s.DeleteUserGroup(ctx, ug.ID); err != nil {
		t.Fatal(err)
	}
	assertAccess(userID, true)
	// Explicit removal of the last membership restores the legacy key policy.
	second.UserIDs = []int64{}
	if err = s.UpdateUserGroup(ctx, second); err != nil {
		t.Fatal(err)
	}
	assertAccess(userID, false)
	if err = s.DeleteUserGroup(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteModelGroup(ctx, premium.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetModelGroup(ctx, mg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing model group: %v", err)
	}
	if _, err = s.GetUserGroup(ctx, ug.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user group: %v", err)
	}
	if err = s.DeleteModelGroup(ctx, mg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat model delete: %v", err)
	}
	if err = s.DeleteUserGroup(ctx, ug.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat user delete: %v", err)
	}
	if _, err = s.GetUserModelAccess(ctx, 99999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	if err = s.UpdateModelGroup(ctx, &model.ModelGroup{ID: 99999999, Name: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing model update: %v", err)
	}
	if err = s.UpdateUserGroup(ctx, &model.UserGroup{ID: 99999999, Name: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user update: %v", err)
	}
}

func TestGroupValidationAndErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ids   []int64
		valid bool
	}{{"valid", []int64{1, 2}, true}, {"   ", nil, false}, {strings.Repeat("x", 201), nil, false}, {"zero", []int64{0}, false}, {"negative", []int64{-1}, false}, {"duplicate", []int64{1, 1}, false}, {"null\x00", nil, false}, {"many", make([]int64, 10001), false}} {
		if got := validGroup(tc.name, tc.ids); got != tc.valid {
			t.Errorf("validGroup(%q)=%v", tc.name, got)
		}
	}
	for _, tc := range []struct{ err, want error }{{sql.ErrNoRows, ErrNotFound}, {&pq.Error{Code: "23505"}, ErrGroupConflict}, {&pq.Error{Code: "23503"}, ErrInvalidGroup}, {&pq.Error{Code: "23514"}, ErrInvalidGroup}, {nil, nil}} {
		if got := groupStoreError(tc.err); !errors.Is(got, tc.want) {
			t.Errorf("error mapping %v => %v want %v", tc.err, got, tc.want)
		}
	}
	s := &Store{}
	if _, err := s.CreateModelGroup(context.Background(), nil); !errors.Is(err, ErrInvalidGroup) {
		t.Fatal(err)
	}
	if _, err := s.CreateUserGroup(context.Background(), nil); !errors.Is(err, ErrInvalidGroup) {
		t.Fatal(err)
	}
	if err := s.UpdateModelGroup(context.Background(), nil); !errors.Is(err, ErrInvalidGroup) {
		t.Fatal(err)
	}
	if err := s.UpdateUserGroup(context.Background(), nil); !errors.Is(err, ErrInvalidGroup) {
		t.Fatal(err)
	}
}

func TestAliasRenameAndDeletePostgres(t *testing.T) {
	s := groupTestStore(t)
	ctx := context.Background()
	chat, err := s.CreateAlias(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	vision, err := s.CreateAlias(ctx, "vision")
	if err != nil {
		t.Fatal(err)
	}
	providerID, err := s.CreateProvider(ctx, &model.Provider{Name: "up", Type: model.ProviderOpenAICompat, BaseURL: "https://api.example.com/v1", TimeoutMS: 30000, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddRouteEntry(ctx, chat, providerID, "gpt-4o-mini", 10); err != nil {
		t.Fatal(err)
	}

	if err = s.RenameAlias(ctx, chat, "chat-v2"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	alias, err := s.GetAlias(ctx, "chat-v2")
	if err != nil || alias.ID != chat {
		t.Fatalf("renamed alias=%+v err=%v", alias, err)
	}
	if _, err = s.GetAlias(ctx, "chat"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old name still resolvable: %v", err)
	}

	if err = s.RenameAlias(ctx, chat, "vision"); !errors.Is(err, ErrAliasConflict) {
		t.Fatalf("expected ErrAliasConflict, got %v", err)
	}
	if err = s.RenameAlias(ctx, 99999999, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing alias, got %v", err)
	}
	if err = s.SetAliasActive(ctx, 99999999, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound toggling a missing alias, got %v", err)
	}

	// Deleting an alias cascades to its route entries.
	if err = s.DeleteAlias(ctx, chat); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err = s.GetAlias(ctx, "chat-v2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("alias survived delete: %v", err)
	}
	routes, err := s.ListRouteEntries(ctx, chat)
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes not cascaded: %v %v", routes, err)
	}
	if err = s.DeleteAlias(ctx, chat); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
	if _, err = s.GetAlias(ctx, "vision"); err != nil {
		t.Fatalf("unrelated alias %d was affected: %v", vision, err)
	}
}
