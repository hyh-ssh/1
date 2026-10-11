package httpapi

import (
	"errors"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/upstream"
)

func TestDescribeUpstreamProbeErrorNamesTheCause(t *testing.T) {
	oversized := strings.Repeat("x", maxUpstreamDetailRunes+50)
	cases := []struct {
		name     string
		err      error
		wantCode string
		wantText string
	}{
		{
			name:     "policy-blocked address",
			err:      &adapter.Error{Type: "blocked", Message: upstream.ErrBlocked.Error()},
			wantCode: "upstream_blocked",
			wantText: "blocked by the network policy",
		},
		{
			name:     "rejected credentials",
			err:      &adapter.Error{StatusCode: 401, Type: "auth", Message: "{\"error\":\"invalid api key\"}"},
			wantCode: "upstream_unauthorized",
			wantText: "invalid api key",
		},
		{
			name:     "provider timeout",
			err:      &adapter.Error{Type: "timeout", Message: "context deadline exceeded"},
			wantCode: "upstream_timeout",
			wantText: "provider timeout",
		},
		{
			name:     "connection failure",
			err:      &adapter.Error{Type: "connection", Message: "Get \"http://10.1.2.3:9/v1/models\": dial tcp: connect: connection refused"},
			wantCode: "upstream_unreachable",
			wantText: "connection refused",
		},
		{
			name:     "rate limited probe",
			err:      &adapter.Error{StatusCode: 429, Type: "rate_limit", Message: "slow down"},
			wantCode: "upstream_rate_limited",
			wantText: "slow down",
		},
		{
			name:     "upstream server error",
			err:      &adapter.Error{StatusCode: 503, Type: "server", Message: "upstream down"},
			wantCode: "model_discovery_failed",
			wantText: "status 503",
		},
		{
			name:     "multiline payload is collapsed",
			err:      &adapter.Error{StatusCode: 400, Type: "client", Message: "bad\nmodel\nname"},
			wantCode: "model_discovery_failed",
			wantText: "bad model name",
		},
		{
			name:     "oversized payload is truncated",
			err:      &adapter.Error{StatusCode: 500, Type: "server", Message: oversized},
			wantCode: "model_discovery_failed",
			wantText: strings.Repeat("x", 40),
		},
		{
			name:     "unclassified error",
			err:      errors.New("json: cannot unmarshal upstream body"),
			wantCode: "model_discovery_failed",
			wantText: "provider model discovery failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, message := describeUpstreamProbeError(tc.err)
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q (message %q)", code, tc.wantCode, message)
			}
			if !strings.Contains(message, tc.wantText) {
				t.Fatalf("message = %q, want it to contain %q", message, tc.wantText)
			}
			if strings.ContainsAny(message, "\r\n") {
				t.Fatalf("message must stay on one line: %q", message)
			}
			if strings.Contains(message, strings.Repeat("x", maxUpstreamDetailRunes+1)) {
				t.Fatalf("message was not truncated: %d bytes", len(message))
			}
		})
	}
}
