package httpapi

import (
	"net/http"
	"net/url"
	"testing"
)

// ④ An id_token whose nonce does not match the transaction fails closed.
func TestOIDCCallbackRejectsNonceMismatch(t *testing.T) {
	idp := newFakeOIDCIdP(t)
	h := newOIDCCallbackHarness(t, idp)
	attempt := h.beginLogin(t)
	idp.setNonce("nonce-does-not-match")

	recorder := h.callbackRequest(t, "code=e2e-code&state="+url.QueryEscape(attempt.state), attempt.stateCookie)
	assertNoSensitiveOIDCValues(t, "nonce mismatch", recorder, oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idp.recordedIDToken())
	assertOIDCErrorRedirect(t, recorder, "invalid_state")

	if len(h.store.boundIssuers) != 0 {
		t.Fatalf("nonce mismatch bound identities: %v", h.store.boundIssuers)
	}
	if len(h.store.sessions) != 0 {
		t.Fatalf("nonce mismatch minted a session: %v", h.store.sessions)
	}
}

// ⑤ A provider-side error=access_denied surfaces as the whitelisted code, with
// no token exchange and no account binding.
func TestOIDCCallbackProviderAccessDenied(t *testing.T) {
	idp := newFakeOIDCIdP(t)
	h := newOIDCCallbackHarness(t, idp)
	attempt := h.beginLogin(t)

	recorder := h.callbackRequest(t, "error=access_denied&state="+url.QueryEscape(attempt.state), attempt.stateCookie)
	assertNoSensitiveOIDCValues(t, "access_denied", recorder, oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idp.recordedIDToken())
	assertOIDCErrorRedirect(t, recorder, "access_denied")

	if calls := idp.tokenCallCount(); calls != 0 {
		t.Fatalf("token endpoint calls = %d, want 0", calls)
	}
	if len(h.store.boundIssuers) != 0 {
		t.Fatalf("access_denied bound identities: %v", h.store.boundIssuers)
	}
	if len(h.store.sessions) != 0 {
		t.Fatalf("access_denied minted a session: %v", h.store.sessions)
	}
}

// ⑥ A first login whose account the store reports as inactive fails with
// account_disabled and no session.
func TestOIDCCallbackDisabledAccount(t *testing.T) {
	idp := newFakeOIDCIdP(t)
	h := newOIDCCallbackHarness(t, idp)
	h.store.setActive(false)
	attempt := h.beginLogin(t)

	recorder := h.callbackRequest(t, "code=e2e-code&state="+url.QueryEscape(attempt.state), attempt.stateCookie)
	assertNoSensitiveOIDCValues(t, "disabled account", recorder, oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idp.recordedIDToken())
	assertOIDCErrorRedirect(t, recorder, "account_disabled")

	if len(h.store.sessions) != 0 {
		t.Fatalf("disabled account received a session: %v", h.store.sessions)
	}
}

// ⑦ Neither the response body nor the Location header of any outcome may carry
// the client secret, the provider tokens or the raw id_token. The same helper
// runs inside every scenario above; this test states the contract explicitly.
func TestOIDCCallbackNeverLeaksProviderSecrets(t *testing.T) {
	idp := newFakeOIDCIdP(t)
	h := newOIDCCallbackHarness(t, idp)
	attempt := h.beginLogin(t)

	success := h.callbackRequest(t, "code=e2e-code&state="+url.QueryEscape(attempt.state), attempt.stateCookie)
	if success.Code != http.StatusFound || success.Header().Get("Location") != "/" {
		t.Fatalf("successful callback = %d %q, want 302 to /", success.Code, success.Header().Get("Location"))
	}
	idToken := idp.recordedIDToken()
	if idToken == "" {
		t.Fatal("token endpoint returned no id_token to check")
	}

	failed := h.callbackRequest(t, "code=e2e-code&state="+url.QueryEscape(attempt.state+"-tampered"), attempt.stateCookie)
	assertOIDCErrorRedirect(t, failed, "invalid_state")

	assertNoSensitiveOIDCValues(t, "success", success,
		oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idToken, "client_secret")
	assertNoSensitiveOIDCValues(t, "failure", failed,
		oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idToken, "client_secret")
}
