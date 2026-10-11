package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"

	llmgateway "github.com/zjvtit/omni-gateway/internal/gateway"
)

func withGatewayRequestMetadata(r *http.Request, principal *Principal, api, modelAlias string, streaming bool) context.Context {
	metadata := llmgateway.RequestMetadata{
		ID:         RequestID(r.Context()),
		API:        api,
		Streaming:  streaming,
		ModelAlias: modelAlias,
		ClientIP:   requestClientIP(r),
		UserAgent:  r.UserAgent(),
	}
	if id, ok := r.Context().Value(recordIDContextKey).(string); ok && id != "" {
		metadata.ID = id
	}
	if principal != nil {
		if principal.User != nil {
			metadata.UserID = principal.User.ID
		}
		if principal.APIKey != nil {
			metadata.APIKeyID = principal.APIKey.ID
		}
	}
	return llmgateway.WithRequestMetadata(r.Context(), metadata)
}

func requestClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
