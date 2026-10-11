package httpapi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Values the fake provider and the callback tests share. The token values are
// deliberately unique strings so a response can be searched for them.
const (
	oidcE2EClientID     = "e2e-client"
	oidcE2EClientSecret = "e2e-client-secret-do-not-leak"
	oidcE2EAccessToken  = "e2e-access-token-do-not-leak"
	oidcE2ERefreshToken = "e2e-refresh-token-do-not-leak"
	oidcE2ERedirectURL  = "https://gateway.example/api/auth/oidc/callback"
	oidcE2ESubject      = "e2e-subject-1"
)

func writeFakeOIDCJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// fakeOIDCIdP is a self-contained OpenID provider for the callback tests: a
// real RSA-2048 key, hand-rolled RS256 ID tokens, a JWKS endpoint and a token
// endpoint that signs whatever nonce the test installed. The discovery
// document's issuer is exactly the httptest URL, which is what go-oidc
// requires for a successful discovery.
type fakeOIDCIdP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string

	mu           sync.Mutex
	nonce        string
	subject      string
	name         string
	lastIDToken  string
	codeVerifier string
	tokenCalls   int
}

func newFakeOIDCIdP(t *testing.T) *fakeOIDCIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	idp := &fakeOIDCIdP{key: key, kid: "e2e-key-1", subject: oidcE2ESubject, name: "E2E User"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeFakeOIDCJSON(w, map[string]any{
			"issuer":                                idp.server.URL,
			"authorization_endpoint":                idp.server.URL + "/oauth/authorize",
			"token_endpoint":                        idp.server.URL + "/oauth/token",
			"jwks_uri":                              idp.server.URL + "/oauth/jwks",
			"response_types_supported":              []string{"code"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/oauth/jwks", func(w http.ResponseWriter, r *http.Request) {
		writeFakeOIDCJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": idp.kid,
			"n":   base64.RawURLEncoding.EncodeToString(idp.key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(idp.key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/oauth/token", idp.token)
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *fakeOIDCIdP) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	idp.mu.Lock()
	idp.tokenCalls++
	idp.codeVerifier = r.PostForm.Get("code_verifier")
	nonce, subject, name := idp.nonce, idp.subject, idp.name
	idp.mu.Unlock()

	idToken, err := idp.signIDToken(nonce, subject, name)
	if err != nil {
		http.Error(w, "could not sign id token", http.StatusInternalServerError)
		return
	}
	idp.mu.Lock()
	idp.lastIDToken = idToken
	idp.mu.Unlock()
	writeFakeOIDCJSON(w, map[string]any{
		"access_token":  oidcE2EAccessToken,
		"refresh_token": oidcE2ERefreshToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"id_token":      idToken,
	})
}

// signIDToken builds an RS256 JWT by hand: header.payload.signature, with the
// kid the JWKS endpoint publishes.
func (idp *fakeOIDCIdP) signIDToken(nonce, subject, name string) (string, error) {
	now := time.Now()
	headerJSON, err := json.Marshal(map[string]any{"alg": "RS256", "kid": idp.kid, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(map[string]any{
		"iss":   idp.server.URL,
		"sub":   subject,
		"aud":   oidcE2EClientID,
		"exp":   now.Add(5 * time.Minute).Unix(),
		"iat":   now.Unix(),
		"nonce": nonce,
		"name":  name,
	})
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (idp *fakeOIDCIdP) setNonce(nonce string) {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.nonce = nonce
}

func (idp *fakeOIDCIdP) recordedCodeVerifier() string {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return idp.codeVerifier
}

func (idp *fakeOIDCIdP) recordedIDToken() string {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return idp.lastIDToken
}

func (idp *fakeOIDCIdP) tokenCallCount() int {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return idp.tokenCalls
}
