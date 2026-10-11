package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// CreateUser inserts a new user. Accounts created here may sign in with a
// password: the caller supplied a real credential. TRUE is written by the SQL
// rather than the Go field, whose zero value would silently disable password
// login for every account created through this path.
func (s *Store) CreateUser(ctx context.Context, u *model.User) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users(username, display_name, role, password_hash, active, password_login_enabled)
		VALUES($1,$2,$3,$4,$5,TRUE) RETURNING id`,
		u.Username, u.DisplayName, string(u.Role), u.PasswordHash, u.Active,
	).Scan(&id)
	return id, err
}

// GetUserByUsername fetches a user by username.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, display_name, role, password_hash, active, password_login_enabled, created_at, updated_at
		FROM users WHERE username=$1`, username,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.Active, &u.PasswordLoginEnabled, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// GetUserByID fetches a user by id.
func (s *Store) GetUserByID(ctx context.Context, id int64) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, display_name, role, password_hash, active, password_login_enabled, created_at, updated_at
		FROM users WHERE id=$1`, id,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.Active, &u.PasswordLoginEnabled, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// ListUsers returns all users ordered by id.
func (s *Store) ListUsers(ctx context.Context) ([]*model.User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, username, display_name, role, password_hash, active, password_login_enabled, created_at, updated_at
		FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.User
	for rows.Next() {
		u := &model.User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.Active, &u.PasswordLoginEnabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser updates mutable user fields.
func (s *Store) UpdateUser(ctx context.Context, u *model.User) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET display_name=$1, role=$2, active=$3, updated_at=now()
		WHERE id=$4`,
		u.DisplayName, string(u.Role), u.Active, u.ID)
	return err
}

// SetUserPassword updates a user's password hash and enables password login.
// Setting a password is the administrator's opt-in for an account that OIDC
// provisioned, so the two changes travel together.
func (s *Store) SetUserPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET password_hash=$1, password_login_enabled=TRUE, updated_at=now()
		WHERE id=$2`, hash, id)
	return err
}

// CountUsers returns the number of users (used for admin bootstrap).
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateSession inserts a server-side session.
func (s *Store) CreateSession(ctx context.Context, id string, userID int64, csrf string, expires time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions(id, user_id, csrf_token, expires_at) VALUES($1,$2,$3,$4)`,
		id, userID, csrf, expires)
	return err
}

// GetSession fetches a session by id.
func (s *Store) GetSession(ctx context.Context, id string) (userID int64, csrf string, expires time.Time, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT user_id, csrf_token, expires_at FROM sessions WHERE id=$1`, id,
	).Scan(&userID, &csrf, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", time.Time{}, ErrNotFound
	}
	return
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, id)
	return err
}

// DeleteUserSessionsExcept revokes every live session of a user except keepID.
// A self-service password change uses it to sign the account out everywhere
// else while the session that made the change stays usable.
func (s *Store) DeleteUserSessionsExcept(ctx context.Context, userID int64, keepID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND id<>$2`, userID, keepID)
	return err
}

// DeleteExpiredSessions removes expired sessions.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	count, err := tag.RowsAffected()
	return count, err
}

// HasOIDCBinding is the actual identity capability, not an inference from a
// placeholder password hash or a display name.
func (s *Store) HasOIDCBinding(ctx context.Context, userID int64) (bool, error) {
	var bound bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM oidc_identities WHERE user_id=$1)`, userID).Scan(&bound)
	return bound, err
}

// ChangeOwnPassword updates the credential and revokes other sessions in one
// transaction. The expected hash prevents racing rotations from overwriting
// a credential whose current-password proof is already stale.
func (s *Store) ChangeOwnPassword(ctx context.Context, userID int64, expectedHash, hash, keepID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE users SET password_hash=$1,password_login_enabled=TRUE,updated_at=now() WHERE id=$2 AND active AND password_hash=$3 AND EXISTS(SELECT 1 FROM sessions WHERE id=$4 AND user_id=$2 AND expires_at>now())`, hash, userID, expectedHash, keepID)
	if err != nil {
		return err
	}
	n, _ := tag.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND id<>$2`, userID, keepID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
