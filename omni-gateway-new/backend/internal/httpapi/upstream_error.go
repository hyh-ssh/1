package httpapi

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zjvtit/omni-gateway/internal/adapter"
)

// maxUpstreamDetailRunes caps how much of an upstream payload is echoed back, so
// a large or multiline body cannot flood the console or the provider row.
const maxUpstreamDetailRunes = 160

// describeUpstreamProbeError maps a failed provider probe to a stable error code
// and an English message. The message names the cause because it is both the
// admin API error the console surfaces in its alert and the value stored in
// providers.last_error for the provider row; a generic "verification failed"
// leaves the administrator with nothing to act on.
func describeUpstreamProbeError(err error) (string, string) {
	var upstreamErr *adapter.Error
	if !errors.As(err, &upstreamErr) {
		return "model_discovery_failed", "provider model discovery failed"
	}
	switch upstreamErr.Type {
	case "blocked":
		return "upstream_blocked", "provider address is blocked by the network policy"
	case "auth":
		return "upstream_unauthorized", "upstream rejected the provider credentials" + upstreamDetail(upstreamErr)
	case "rate_limit":
		return "upstream_rate_limited", "upstream rate limited the probe" + upstreamDetail(upstreamErr)
	case "timeout":
		return "upstream_timeout", "upstream did not answer within the provider timeout"
	case "connection":
		return "upstream_unreachable", "could not connect to the upstream" + upstreamDetail(upstreamErr)
	default:
		if upstreamErr.StatusCode >= 400 {
			return "model_discovery_failed", fmt.Sprintf("upstream returned status %d", upstreamErr.StatusCode) + upstreamDetail(upstreamErr)
		}
		return "model_discovery_failed", "provider model discovery failed"
	}
}

// upstreamDetail renders the upstream's own message on a single line and
// truncates it on a rune boundary.
func upstreamDetail(err *adapter.Error) string {
	detail := strings.Join(strings.Fields(err.Message), " ")
	if detail == "" {
		return ""
	}
	if runes := []rune(detail); len(runes) > maxUpstreamDetailRunes {
		detail = string(runes[:maxUpstreamDetailRunes]) + "…"
	}
	return ": " + detail
}
