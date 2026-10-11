package model

// StatsFilter scopes dashboard and request analytics queries.
type StatsFilter struct {
	RequestID        string
	SettlementStatus string
	API              string
	ModelGroupID     int64
	UserID           int64
	APIKeyID         int64
	ProviderID       int64
	ProviderModel    string
	ModelAlias       string
	Status           RequestStatus
	From             Timestamp
	To               Timestamp
	Limit            int
	Offset           int
}

// StatsSummary is the aggregate dashboard payload.
type StatsSummary struct {
	Trend                 []TrendPoint     `json:"trend"`
	ErrorTypes            map[string]int64 `json:"error_types"`
	Requests              int64            `json:"requests"`
	Success               int64            `json:"success"`
	Errors                int64            `json:"errors"`
	SuccessRate           float64          `json:"success_rate"`
	InputTokens           int64            `json:"input_tokens"`
	OutputTokens          int64            `json:"output_tokens"`
	TotalTokens           int64            `json:"total_tokens"`
	Cost                  Money            `json:"cost"`    // upstream cost total, in CNY
	Credits               Credits          `json:"credits"` // credit charge total
	AvgLatencyMS          float64          `json:"avg_latency_ms"`
	AvgTTFTMS             float64          `json:"avg_ttft_ms"`
	FailoverCount         int64            `json:"failover_count"`
	EstimatedCount        int64            `json:"estimated_count"`
	P50LatencyMS          float64          `json:"p50_latency_ms"`
	P95LatencyMS          float64          `json:"p95_latency_ms"`
	P99LatencyMS          float64          `json:"p99_latency_ms"`
	P50TTFTMS             float64          `json:"p50_ttft_ms"`
	P95TTFTMS             float64          `json:"p95_ttft_ms"`
	P99TTFTMS             float64          `json:"p99_ttft_ms"`
	AvgUpstreamLatencyMS  float64          `json:"avg_upstream_latency_ms"`
	AvgOutputTokensPerSec float64          `json:"avg_output_tokens_per_sec"`
	AvgCostPerRequest     Money            `json:"avg_cost_per_request"`
	AvgCreditsPerRequest  Credits          `json:"avg_credits_per_request"`
	FailedAttemptCost     Money            `json:"failed_attempt_cost"`
	SettlementErrors      int64            `json:"settlement_errors"`
	Routes                []RouteStats     `json:"routes"`
}

type TrendPoint struct {
	Period   Timestamp `json:"period"`
	Requests int64     `json:"requests"`
	Success  int64     `json:"success"`
	Cost     Money     `json:"cost"`    // upstream cost in this period, in CNY
	Credits  Credits   `json:"credits"` // credits charged in this period
}

// RouteStats measures upstream attempts, so failed failover routes stay visible.
type RouteStats struct {
	ModelAlias       string  `json:"model_alias"`
	ProviderID       int64   `json:"provider_id"`
	ProviderModel    string  `json:"provider_model"`
	Attempts         int64   `json:"attempts"`
	Success          int64   `json:"success"`
	Errors           int64   `json:"errors"`
	SuccessRate      float64 `json:"success_rate"`
	AvgLatencyMS     float64 `json:"avg_latency_ms"`
	P95LatencyMS     float64 `json:"p95_latency_ms"`
	Cost             Money   `json:"cost"`    // upstream cost of these attempts, in CNY
	Credits          Credits `json:"credits"` // credits charged for these attempts
	FailedCost       Money   `json:"failed_cost"`
	SettlementErrors int64   `json:"settlement_errors"`
}
