// Package upstream centralizes network policy for provider calls and probes.
package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrBlocked = errors.New("upstream address is blocked by network policy")

// ValidateURL rejects credential-bearing and non-HTTP provider URLs.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("base_url must be an http(s) URL without credentials, query or fragment")
	}
	return nil
}

func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	// Shared address space, metadata/platform and reserved IPv4 ranges.
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2002::/16"),
}

type transport struct{ base *http.Transport }

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.User != nil {
		return nil, ErrBlocked
	}
	return t.base.RoundTrip(r)
}
func (t *transport) CloseIdleConnections() { t.base.CloseIdleConnections() }

// NewClient pins DNS results at dial time, so a later resolution cannot turn
// a permitted name into an internal destination. Redirects and environment
// proxies are disabled; all credentialed calls use this transport.
func NewClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.MaxIdleConns = 100
	t.MaxIdleConnsPerHost = 20
	t.MaxConnsPerHost = 100
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 30 * time.Second
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrBlocked
		}
		if strings.Contains(host, "%") {
			return nil, ErrBlocked
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("upstream DNS lookup failed")
		}
		for _, ip := range ips {
			if !allowPrivate && !publicAddress(ip) {
				return nil, ErrBlocked
			}
		}
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, errors.New("upstream connection failed")
	}
	return &http.Client{Transport: &transport{base: t}, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("upstream redirects are disabled") }}
}
