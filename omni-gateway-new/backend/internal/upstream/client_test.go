package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestAddressPolicy(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1", "::ffff:127.0.0.1", "100.100.100.200", "0.0.0.0", "224.0.0.1", "240.0.0.1", "64:ff9b::7f00:1"} {
		if publicAddress(netip.MustParseAddr(value)) {
			t.Errorf("allowed %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(value)) {
			t.Errorf("blocked public %s", value)
		}
	}
}

func TestClientBlocksPrivateAndRedirects(t *testing.T) {
	called := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; http.Redirect(w, r, "/target", 302) }))
	defer s.Close()
	c := NewClient(false)
	defer c.CloseIdleConnections()
	if _, err := c.Get(s.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected blocked, got %v", err)
	}
	if called != 0 {
		t.Fatal("private server received request")
	}
	c = NewClient(true)
	defer c.CloseIdleConnections()
	resp, err := c.Get(s.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || called != 1 {
		t.Fatalf("redirect followed: calls=%d err=%v", called, err)
	}
}

func TestValidateURL(t *testing.T) {
	for _, value := range []string{"file:///x", "https://user:pass@example.com", "https://example.com?key=x", "https://example.com#x", "//example.com", "http://"} {
		if ValidateURL(value) == nil {
			t.Errorf("allowed %s", value)
		}
	}
	if err := ValidateURL("https://example.com/v1"); err != nil {
		t.Fatal(err)
	}
}
