package model

import "time"

// RequestStatus is the final status of a gateway request.
type RequestStatus string

const (
	StatusSuccess RequestStatus = "success"
	StatusError   RequestStatus = "error"
)

// Request is the top-level record of one gateway API call.
type Request struct {
	ID                 string            `json:"id"` // request id (x-request-id)
	UserID             int64             `json:"user_id"`
	APIKeyID           int64             `json:"api_key_id"`
	API                string            `json:"api"` // "chat_completions" | "responses" | "models"
	Streaming          bool              `json:"streaming"`
	ModelAlias         string            `json:"model_alias"`
	Status             RequestStatus     `json:"status"`
	StatusCode         int               `json:"status_code"`
	ErrorType          string            `json:"error_type,omitempty"`
	ErrorSummary       string            `json:"error_summary,omitempty"`
	ErrorSuggestion    string            `json:"error_suggestion,omitempty"`
	SettlementStatus   string            `json:"settlement_status"`
	Usage              Usage             `json:"usage"`
	Cost               Money             `json:"cost"`    // total upstream cost, in CNY (all attempts)
	Credits            Credits           `json:"credits"` // charged to the caller, in credits (success only)
	TotalLatencyMS     int64             `json:"total_latency_ms"`
	UpstreamLatencyMS  int64             `json:"upstream_latency_ms"`
	TTFTMS             int64             `json:"ttft_ms"`
	OutputTokensPerSec float64           `json:"output_tokens_per_sec"`
	ClientIP           string            `json:"client_ip,omitempty"`
	UserAgent          string            `json:"user_agent,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
}

// Attempt is one upstream call within a request (for failover).
type Attempt struct {
	ID               int64     `json:"id"`
	SettlementID     string    `json:"settlement_id"`
	SettlementError  string    `json:"settlement_error,omitempty"`
	SettlementStatus string    `json:"settlement_status"`
	RequestID        string    `json:"request_id"`
	ProviderID       int64     `json:"provider_id"`
	ProviderModel    string    `json:"provider_model"`
	Priority         int       `json:"priority"`
	Status           string    `json:"status"` // "success" | "error"
	StatusCode       int       `json:"status_code"`
	ErrorType        string    `json:"error_type,omitempty"`
	ErrorSummary     string    `json:"error_summary,omitempty"`
	Usage            Usage     `json:"usage"`
	Cost             Money     `json:"cost"`
	LatencyMS        int64     `json:"latency_ms"`
	TTFTMS           int64     `json:"ttft_ms"`
	CreatedAt        time.Time `json:"created_at"`
}

// LedgerEntry is an immutable usage ledger record.
type LedgerEntry struct {
	ID            int64     `json:"id"`
	SettlementID  string    `json:"settlement_id"`
	RequestID     string    `json:"request_id"`
	UserID        int64     `json:"user_id"`
	APIKeyID      int64     `json:"api_key_id"`
	ModelAlias    string    `json:"model_alias"`
	ProviderID    int64     `json:"provider_id"`
	ProviderModel string    `json:"provider_model"`
	Meter         string    `json:"meter"`
	Quantity      int64     `json:"quantity"`
	Cost          Money     `json:"cost"`    // upstream cost of this meter, in CNY
	Credits       Credits   `json:"credits"` // credit charge of this meter
	PriceVersion  int64     `json:"price_version"`
	Estimated     bool      `json:"estimated"`
	CreatedAt     time.Time `json:"created_at"`
}

// AuditEntry is an audit log record.
type AuditEntry struct {
	ID           int64          `json:"id"`
	ActorID      int64          `json:"actor_id"`
	ActorName    string         `json:"actor_name"`
	Action       string         `json:"action"`
	Resource     string         `json:"resource"`
	ResourceID   string         `json:"resource_id,omitempty"`
	Detail       string         `json:"detail,omitempty"`
	IP           string         `json:"ip,omitempty"`
	Result       string         `json:"result"`
	ResourceName string         `json:"resource_name,omitempty"`
	Before       map[string]any `json:"before,omitempty"`
	After        map[string]any `json:"after,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// AuditFilter uses inclusive UTC time bounds and stable newest-first paging.
type AuditFilter struct {
	From     Timestamp
	To       Timestamp
	ActorID  int64
	Resource string
	Action   string
	Result   string
	Limit    int
	Offset   int
}
