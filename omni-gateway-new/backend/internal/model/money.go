package model

import (
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
)

// The gateway keeps two independent accounts that never share a unit:
//
//   - Credits are what gateway callers are charged: the unit a customer spends
//     from their quota, not any currency. Every user-facing amount (credit
//     price, ledger charge, `credit_limit`) is denominated in credits and is
//     fractional, quantized to CreditsScale decimal places.
//   - Money is what upstream providers bill. Every cost amount (upstream cost
//     price, `cost_limit`) stays in real currency (CNY).
//
// Both are fixed-point decimals; floating point is never used for settlement.
// Because the two are aliases of one decimal type, the compiler cannot tell a
// credit from a yuan: parameter order (cost, then credits) is the only thing
// keeping them apart, and an amount must never be added to or subtracted from
// one of the other kind. A "profit" spanning both is meaningless and is
// deliberately not computed anywhere.

// Money is a fixed-point decimal amount in CNY.
type Money = decimal.Decimal

// ZeroMoney is the zero monetary amount.
var ZeroMoney = decimal.Zero

// NewMoney parses a decimal string into Money.
func NewMoney(s string) (Money, error) {
	return decimal.NewFromString(s)
}

// MustMoney parses a decimal string, panicking on error (for tests/seeds).
func MustMoney(s string) Money {
	return decimal.RequireFromString(s)
}

// NewMoneyInt converts an integer quantity into a fixed-point monetary value.
func NewMoneyInt(value int64) Money { return decimal.NewFromInt(value) }

// CreditsScale is how many decimal places a credit amount keeps. It bounds the
// charged amount, not a credit unit rate: a credit price per token is a rate
// and legitimately needs more precision than the amount it produces.
const CreditsScale = 3

// Credits is a fixed-point amount of the unit charged to gateway callers.
type Credits = decimal.Decimal

// ZeroCredits is the zero credit amount.
var ZeroCredits = decimal.Zero

// NewCredits parses a decimal string into Credits.
func NewCredits(s string) (Credits, error) {
	return decimal.NewFromString(s)
}

// MustCredits parses a decimal string, panicking on error (for tests/seeds).
func MustCredits(s string) Credits {
	return decimal.RequireFromString(s)
}

// NewCreditsInt converts an integer quantity into a credit amount.
func NewCreditsInt(value int64) Credits { return decimal.NewFromInt(value) }

// RoundCredits quantizes a credit amount to CreditsScale decimal places,
// rounding halves away from zero.
//
// Every credit amount that is persisted or returned passes through here, so
// the ledger rows, the request total and the budget sums derived from them all
// agree on one precision. Unit rates are not rounded.
func RoundCredits(v Credits) Credits { return v.Round(CreditsScale) }

// Usage captures token and other metered quantities for a single attempt or
// request. Meter keys are extensible (input, output, cache_input, reasoning,
// image, audio, requests, ...).
type Usage struct {
	Meters map[string]int64 `json:"meters"`
	// Estimated marks usage that was locally estimated rather than reported
	// by the upstream provider.
	Estimated bool `json:"estimated"`
}

// NewUsage returns an empty usage with an initialized meter map.
func NewUsage() Usage {
	return Usage{Meters: map[string]int64{}}
}

// Add returns the sum of two usages (meter-wise).
func (u Usage) Add(o Usage) Usage {
	out := NewUsage()
	for k, v := range u.Meters {
		out.Meters[k] += v
	}
	for k, v := range o.Meters {
		out.Meters[k] += v
	}
	out.Estimated = u.Estimated || o.Estimated
	return out
}

// TotalTokens prefers the provider total because cache/reasoning meters may
// be subsets of input/output. Older records without a total retain their sum.
func (u Usage) TotalTokens() int64 {
	if total := u.Meters["total"]; total > 0 {
		return total
	}
	var t int64
	for k, v := range u.Meters {
		switch k {
		case "input", "output", "cache_input", "cache_creation", "reasoning":
			t += v
		}
	}
	return t
}

// Get returns a meter value by key.
func (u Usage) Get(key string) int64 { return u.Meters[key] }

// Set sets a meter value.
func (u Usage) Set(key string, v int64) { u.Meters[key] = v }

// MarshalJSON renders usage as a stable object.
func (u Usage) MarshalJSON() ([]byte, error) {
	type alias struct {
		Meters    map[string]int64 `json:"meters"`
		Estimated bool             `json:"estimated"`
		Source    string           `json:"source"`
	}
	source := "unreported"
	if len(u.Meters) > 0 {
		source = "upstream"
	}
	if u.Estimated {
		source = "text_bytes_v1"
	}
	return json.Marshal(alias{Meters: u.Meters, Estimated: u.Estimated, Source: source})
}

// UnmarshalJSON parses usage.
func (u *Usage) UnmarshalJSON(b []byte) error {
	type alias struct {
		Meters    map[string]int64 `json:"meters"`
		Estimated bool             `json:"estimated"`
	}
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if a.Meters == nil {
		a.Meters = map[string]int64{}
	}
	u.Meters = a.Meters
	u.Estimated = a.Estimated
	return nil
}

// Timestamp is a UTC timestamp helper.
type Timestamp = time.Time

// Now returns the current UTC time.
func Now() time.Time { return time.Now().UTC() }
