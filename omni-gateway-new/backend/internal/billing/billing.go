package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/zjvtit/omni-gateway/internal/background"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// PriceStore supplies immutable price snapshots. A missing snapshot means the
// observed usage cannot be priced: no ledger row is written and the result is
// flagged Unpriced so a missing price version stays observable instead of
// silently becoming free traffic.
type PriceStore interface {
	GetLatestPriceVersion(context.Context, string) (*model.PriceVersion, error)
}

// LedgerStore persists immutable usage ledger rows.
type LedgerStore interface {
	InsertLedgerEntries(context.Context, []*model.LedgerEntry) error
}

// Result contains the settlement totals and the immutable rows generated for a
// single upstream attempt.
type Result struct {
	SettlementID     string
	SettlementStatus string
	Persisted        bool
	Cost             model.Money   // upstream cost of this attempt, in CNY
	Credits          model.Credits // credits charged to the caller (success only)
	Entries          []*model.LedgerEntry
	// Unpriced reports that the attempt carried billable usage but no price
	// version matched, so no ledger row was produced. It never invents a
	// zero-amount charge; callers surface it as an observability signal.
	Unpriced bool
	// ZeroCreditMeters counts meters that carried a positive quantity on a
	// successful attempt yet produced a zero credit charge: the credit rate is
	// absent for a meter with upstream cost, or a positive credit rate quantizes
	// away at the three-decimal amount scale. Explicit zero credit rates waive
	// credits, even with positive cost, and are not anomalies. A meter neither
	// ledger prices is not counted.
	ZeroCreditMeters int
}

// Service calculates the upstream cost and the caller credit charge from a
// versioned price snapshot and optionally writes the resulting ledger rows. It
// does not mutate price versions.
type Service struct {
	prices  PriceStore
	ledgers LedgerStore
	writer  *background.Writer
}

func NewService(prices PriceStore, ledgers LedgerStore) *Service {
	return &Service{prices: prices, ledgers: ledgers, writer: background.New(background.Options{})}
}

type attemptKey struct{}
type attemptMetadata struct {
	id string
	at time.Time
}

// WithAttempt keeps one immutable identifier/timestamp across persistence retries.
func WithAttempt(ctx context.Context, id string, at time.Time) context.Context {
	return context.WithValue(ctx, attemptKey{}, attemptMetadata{id: id, at: at})
}

func NewSettlementID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("secure settlement identifier unavailable")
	}
	return hex.EncodeToString(id[:])
}

func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.writer.Close(ctx)
}
func (s *Service) PersistenceStats() background.Stats {
	if s == nil {
		return background.Stats{}
	}
	return s.writer.Stats()
}

// Settle calculates the charges for one attempt. Cost accumulates for every
// attempt and stays in CNY; Credits (the caller-facing charge) is emitted only
// when success is true, so an upstream failure costs the platform without being
// billed to the caller. When usage is billable but no price version matches,
// Settle writes nothing and returns a Result with Unpriced set.
func (s *Service) Settle(ctx context.Context, requestID string, userID, apiKeyID int64, alias, providerModel string, providerID int64, usage model.Usage, success bool) (*Result, error) {
	meta, _ := ctx.Value(attemptKey{}).(attemptMetadata)
	if meta.id == "" {
		meta.id = NewSettlementID()
	}
	if meta.at.IsZero() {
		meta.at = time.Now().UTC()
	}
	result := &Result{SettlementID: meta.id, Cost: model.ZeroMoney, Credits: model.ZeroCredits, SettlementStatus: "none"}
	positive := false
	for _, quantity := range usage.Meters {
		positive = positive || quantity > 0
	}
	if !positive {
		// Nothing billable was observed, so there is no price to look up. This
		// is the normal shape of a failed attempt that has no reported usage.
		return result, nil
	}
	if s == nil || s.prices == nil {
		// No price store is configured, so observed usage cannot be priced.
		result.Unpriced = true
		result.SettlementStatus = "unpriced"
		return result, nil
	}
	price, err := latestPrice(ctx, s.prices, providerModel, alias)
	if err != nil {
		return nil, err
	}
	if price == nil {
		// Billable usage with no matching price version: no ledger row is
		// written and the caller records the attempt as unpriced.
		result.Unpriced = true
		result.SettlementStatus = "unpriced"
		return result, nil
	}
	result.SettlementStatus = "settled"
	switch model.PriceStatus(price) {
	case "free":
		result.SettlementStatus = "free"
	case "credits_free":
		result.SettlementStatus = "credits_free"
	case "unpriced":
		result.SettlementStatus = "unpriced"
	}
	for meter, quantity := range usage.Meters {
		if quantity <= 0 {
			continue
		}
		costUnit, costPriced := price.Cost[meter]
		creditUnit, creditPriced := price.Credits[meter]
		if meter != "total" && !costPriced && !creditPriced {
			result.SettlementStatus = "unpriced"
		}
		// The upstream cost keeps full decimal precision in CNY. The credit
		// charge is quantized to model.CreditsScale so every persisted amount
		// agrees with the totals derived from it; the unit rates themselves
		// are rates, not amounts, and are not rounded.
		cost := costUnit.Mul(model.NewMoneyInt(quantity))
		credits := model.ZeroCredits
		if success {
			credits = model.RoundCredits(creditUnit.Mul(model.NewCreditsInt(quantity)))
			if credits.IsZero() && ((!creditPriced && !costUnit.IsZero()) || creditUnit.IsPositive()) {
				// Missing credits with upstream cost, or a positive rate
				// rounded away, remain observable pricing holes. An explicit zero
				// credit rate intentionally waives credits. Unrated aggregate meters
				// (such as "total") keep their existing non-billing behavior.
				result.ZeroCreditMeters++
			}
		}
		result.Cost = result.Cost.Add(cost)
		result.Credits = result.Credits.Add(credits)
		result.Entries = append(result.Entries, &model.LedgerEntry{
			SettlementID:  meta.id,
			CreatedAt:     meta.at,
			RequestID:     requestID,
			UserID:        userID,
			APIKeyID:      apiKeyID,
			ModelAlias:    alias,
			ProviderID:    providerID,
			ProviderModel: providerModel,
			Meter:         meter,
			Quantity:      quantity,
			Cost:          cost,
			Credits:       credits,
			PriceVersion:  int64(price.Version),
			Estimated:     usage.Estimated,
		})
	}
	if s.ledgers != nil && len(result.Entries) > 0 {
		// Wait for bounded retries so a confirmed commit is never also reserved
		// by the local budget policy. Always return calculated totals on failure.
		entries := append([]*model.LedgerEntry(nil), result.Entries...)
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 16*time.Second)
		defer cancel()
		if err := s.writer.Write(writeCtx, func(ctx context.Context) error { return s.ledgers.InsertLedgerEntries(ctx, entries) }); err != nil {
			result.SettlementStatus = "failed"
			return result, fmt.Errorf("persist settlement: %w", err)
		}
		result.Persisted = true
	}
	return result, nil
}

func latestPrice(ctx context.Context, prices PriceStore, providerModel, alias string) (*model.PriceVersion, error) {
	if providerModel != "" {
		price, err := prices.GetLatestPriceVersion(ctx, providerModel)
		if err == nil && price != nil {
			return price, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("load provider model price: %w", err)
		}
	}
	if alias == "" {
		return nil, nil
	}
	price, err := prices.GetLatestPriceVersion(ctx, alias)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load alias price: %w", err)
	}
	return price, nil
}
