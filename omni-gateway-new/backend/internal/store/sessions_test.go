package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// selfSessionTestStore returns the shared PostgreSQL harness defined in
// groups_test.go: every test gets a private schema and the whole test is
// skipped when TEST_DATABASE_URL is unset.
func selfSessionTestStore(t *testing.T) *Store {
	t.Helper()
	return groupTestStore(t)
}

// A password change signs the account out on every other device, which only
// holds if the delete really filters on the owner *and* spares the session that
// made the change. Getting the filter wrong is silent: the password is already
// rotated and the old sessions keep working.
func TestDeleteUserSessionsExceptKeepsOnlyTheCurrentSession(t *testing.T) {
	ctx := context.Background()
	s := selfSessionTestStore(t)

	rotator, err := s.CreateUser(ctx, &model.User{Username: "rotator", Role: model.RoleUser, PasswordHash: "hash", Active: true})
	if err != nil {
		t.Fatalf("CreateUser(rotator): %v", err)
	}
	bystander, err := s.CreateUser(ctx, &model.User{Username: "bystander", Role: model.RoleUser, PasswordHash: "hash", Active: true})
	if err != nil {
		t.Fatalf("CreateUser(bystander): %v", err)
	}

	expires := time.Now().UTC().Add(time.Hour)
	sessions := []struct {
		id     string
		userID int64
	}{
		{id: "keep", userID: rotator},
		{id: "drop-one", userID: rotator},
		{id: "drop-two", userID: rotator},
		{id: "other-user", userID: bystander},
	}
	for _, session := range sessions {
		if err := s.CreateSession(ctx, session.id, session.userID, "csrf-"+session.id, expires); err != nil {
			t.Fatalf("CreateSession(%s): %v", session.id, err)
		}
	}

	if err := s.DeleteUserSessionsExcept(ctx, rotator, "keep"); err != nil {
		t.Fatalf("DeleteUserSessionsExcept: %v", err)
	}

	for _, id := range []string{"keep", "other-user"} {
		if _, _, _, err := s.GetSession(ctx, id); err != nil {
			t.Fatalf("session %q should have survived, got %v", id, err)
		}
	}
	for _, id := range []string{"drop-one", "drop-two"} {
		if _, _, _, err := s.GetSession(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("session %q should have been revoked, got %v", id, err)
		}
	}
}
