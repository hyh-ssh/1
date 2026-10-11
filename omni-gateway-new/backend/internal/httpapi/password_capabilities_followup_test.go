package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type passwordCapabilitiesFollowupStore struct {
	*selfTestStore
	bound bool
}

func (s *passwordCapabilitiesFollowupStore) HasOIDCBinding(context.Context, int64) (bool, error) {
	return s.bound, nil
}

type passwordCapabilitiesFollowupPayload struct {
	User struct {
		Username             string `json:"username"`
		PasswordLoginEnabled bool   `json:"password_login_enabled"`
		Capabilities         struct {
			LocalEnabled         bool  `json:"local_enabled"`
			CanSetWithoutCurrent bool  `json:"can_set_without_current"`
			CanRecover           *bool `json:"can_recover"`
		} `json:"password_capabilities"`
	} `json:"user"`
}

func TestCoverageFollowupPasswordRecoveryCapabilityAndAnonymousPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name, probePassword  string
		localEnabled         bool
		bound                bool
		canSetWithoutCurrent bool
	}{
		{name: "local password", probePassword: "wrong-password", localEnabled: true},
		{name: "OIDC-bound without local password", probePassword: "alice-password", bound: true, canSetWithoutCurrent: true},
		{name: "unbound without local password", probePassword: "alice-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &passwordCapabilitiesFollowupStore{selfTestStore: newSelfTestStore(t), bound: tc.bound}
			store.usersByID[2].PasswordLoginEnabled = tc.localEnabled
			store.sessions["password-capability-session"] = fakeSession{userID: 2, csrf: "csrf", expires: time.Now().Add(time.Hour)}
			handler := NewHandlerWithAuth(nil, store, AuthConfig{})
			cookie := &http.Cookie{Name: sessionCookieName, Value: "password-capability-session"}

			assertCapabilities := func(path string) {
				t.Helper()
				response := getSelf(t, handler, cookie, path)
				if response.Code != http.StatusOK {
					t.Fatalf("%s returned %d: %s", path, response.Code, response.Body.String())
				}
				var got passwordCapabilitiesFollowupPayload
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.User.Username != "alice" || got.User.PasswordLoginEnabled != tc.localEnabled ||
					got.User.Capabilities.LocalEnabled != tc.localEnabled ||
					got.User.Capabilities.CanSetWithoutCurrent != tc.canSetWithoutCurrent ||
					got.User.Capabilities.CanRecover == nil || *got.User.Capabilities.CanRecover {
					t.Fatalf("unexpected authenticated password capability payload at %s", path)
				}
			}
			assertCapabilities("/api/auth/me")
			assertCapabilities("/api/self/summary")

			for _, path := range []string{"/api/auth/me", "/api/self/summary"} {
				response := getSelf(t, handler, nil, path)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("anonymous %s returned %d", path, response.Code)
				}
				for _, privateField := range []string{"alice", "password_capabilities", "can_recover", "can_set_without_current", "password_login_enabled"} {
					if strings.Contains(response.Body.String(), privateField) {
						t.Fatalf("anonymous %s disclosed account capability metadata", path)
					}
				}
			}

			var anonymousErrors []struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			for _, username := range []string{"alice", "unknown-account"} {
				body, err := json.Marshal(map[string]string{"username": username, "password": tc.probePassword})
				if err != nil {
					t.Fatal(err)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body))))
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("anonymous login returned %d", response.Code)
				}
				for _, privateField := range []string{"alice", "password_capabilities", "can_recover", "can_set_without_current", "password_login_enabled"} {
					if strings.Contains(response.Body.String(), privateField) {
						t.Fatal("anonymous login disclosed account capability metadata")
					}
				}
				var envelope struct {
					Error struct {
						Type    string `json:"type"`
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				anonymousErrors = append(anonymousErrors, envelope.Error)
			}
			if len(anonymousErrors) != 2 || !reflect.DeepEqual(anonymousErrors[0], anonymousErrors[1]) {
				t.Fatal("anonymous login distinguished the account's password capability from an unknown account")
			}
		})
	}
}
