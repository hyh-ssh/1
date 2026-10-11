package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// Fail only after the dispatcher has authenticated the active administrator.
// The request traverses the public router, CSRF gate and actual signing path.
type adminKeyReauthStore struct {
	*adminTestStore
	failure             string
	sessionReads        int
	userReads           int
	initialUserResolved bool
}

func (s *adminKeyReauthStore) GetSession(ctx context.Context, id string) (int64, string, time.Time, error) {
	s.sessionReads++
	userID, csrf, expires, err := s.fakeAuthStore.GetSession(ctx, id)
	if s.sessionReads > 1 {
		switch s.failure {
		case "expired session":
			expires = time.Now().Add(-time.Hour)
		case "session store error":
			return 0, "", time.Time{}, errors.New("session lookup unavailable")
		}
	}
	return userID, csrf, expires, err
}

func (s *adminKeyReauthStore) GetUserByID(ctx context.Context, id int64) (*model.User, error) {
	s.userReads++
	if s.userReads > 1 {
		switch s.failure {
		case "user store error":
			return nil, errors.New("user lookup unavailable")
		case "missing user":
			return nil, nil
		}
	}
	user, err := s.fakeAuthStore.GetUserByID(ctx, id)
	if s.userReads == 1 {
		s.initialUserResolved = err == nil && user != nil && user.Active && user.Role == model.RoleAdmin
	}
	return user, err
}

func TestAdminKeySigningRejectsFailedSecondAuthentication(t *testing.T) {
	for _, failure := range []string{"expired session", "session store error", "user store error", "missing user"} {
		t.Run(failure, func(t *testing.T) {
			s := &adminKeyReauthStore{adminTestStore: newAdminTestStore(), failure: failure}
			s.sessions["live-admin"] = fakeSession{userID: 1, csrf: "csrf", expires: time.Now().Add(time.Hour)}
			handler := NewHandlerWithAuth(nil, s, AuthConfig{})
			request := httptest.NewRequest(http.MethodPost, "/api/admin/api-keys", strings.NewReader(`{"name":"must-not-be-issued"}`))
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "live-admin"})
			request.Header.Set("X-CSRF-Token", "csrf")
			response := httptest.NewRecorder()
			func() {
				defer func() {
					if panicValue := recover(); panicValue != nil {
						t.Errorf("failed second authentication panicked during signing: %v", panicValue)
					}
				}()
				handler.ServeHTTP(response, request)
			}()
			if !s.initialUserResolved || s.sessionReads != 2 {
				t.Fatalf("fixture did not pass initial authentication then recheck: initial=%v reads=%d", s.initialUserResolved, s.sessionReads)
			}
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"authentication_required"`) {
				t.Fatalf("failed recheck must return a clear authentication error: %d %s", response.Code, response.Body.String())
			}
			if len(s.keys) != 0 || s.keysNext != 1 || strings.Contains(response.Body.String(), `"secret"`) {
				t.Fatal("failed recheck issued or exposed an API key")
			}
		})
	}
}
