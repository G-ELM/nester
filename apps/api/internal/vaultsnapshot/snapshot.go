// Package vaultsnapshot periodically exports a signed, point-in-time
// snapshot of every vault's balance (and a per-currency ledger total) to
// storage independent of the primary Postgres database — so if the primary
// store is ever compromised or corrupted, there is an immutable, tamper-
// evident record of what the balances should have been at a known time,
// usable for forensic reconciliation.
//
// This complements internal/audit's tamper-evident hash-chain log: audit
// proves the *sequence of actions* wasn't altered; vaultsnapshot proves the
// *resulting balances* at a point in time weren't altered, from a copy that
// lives outside the database those actions were recorded in.
package vaultsnapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// VaultBalance is one vault's balance state at snapshot time. Deliberately
// narrow — just enough to reconcile "what should this vault's balance be"
// during forensic recovery, not a full transaction history (that's what the
// primary database and internal/audit's chain are for; this snapshot exists
// for when those are unavailable or suspect).
type VaultBalance struct {
	VaultID         uuid.UUID       `json:"vault_id"`
	ContractAddress string          `json:"contract_address"`
	Currency        string          `json:"currency"`
	TotalDeposited  decimal.Decimal `json:"total_deposited"`
	CurrentBalance  decimal.Decimal `json:"current_balance"`
	YieldEarned     decimal.Decimal `json:"yield_earned"`
	FeesPaid        decimal.Decimal `json:"fees_paid"`
	Status          string          `json:"status"`
}

// Fetcher supplies the current balance state of every vault to snapshot.
// Narrow and decoupled from internal/domain/vault's full Vault type (same
// "narrow interface" reasoning as scheduler.ChainVerifier) so this package
// has no dependency on the domain/repository layers — main.go adapts
// vault.Vault into VaultBalance at the wiring point.
type Fetcher interface {
	ListVaultBalances(ctx context.Context) ([]VaultBalance, error)
}

// FetcherFunc adapts a plain function to Fetcher.
type FetcherFunc func(ctx context.Context) ([]VaultBalance, error)

func (f FetcherFunc) ListVaultBalances(ctx context.Context) ([]VaultBalance, error) { return f(ctx) }

// Snapshot is the exported, signable unit: every vault's balance plus the
// aggregate ledger total per currency, as of TakenAt.
type Snapshot struct {
	TakenAt       time.Time                  `json:"taken_at"`
	Vaults        []VaultBalance             `json:"vaults"`
	TotalByCurrency map[string]decimal.Decimal `json:"total_by_currency"`
	VaultCount    int                        `json:"vault_count"`
}

// Build fetches the current balance of every vault and aggregates the
// per-currency ledger total. Vaults are sorted by ID so the resulting JSON
// (and therefore its signature, see Envelope) is deterministic across runs
// regardless of the fetcher's own row order.
func Build(ctx context.Context, fetcher Fetcher, takenAt time.Time) (Snapshot, error) {
	balances, err := fetcher.ListVaultBalances(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("vaultsnapshot: list balances: %w", err)
	}

	sort.Slice(balances, func(i, j int) bool {
		return balances[i].VaultID.String() < balances[j].VaultID.String()
	})

	totals := make(map[string]decimal.Decimal)
	for _, b := range balances {
		totals[b.Currency] = totals[b.Currency].Add(b.CurrentBalance)
	}

	return Snapshot{
		TakenAt:         takenAt.UTC(),
		Vaults:          balances,
		TotalByCurrency: totals,
		VaultCount:      len(balances),
	}, nil
}

// canonicalJSON serializes the snapshot with sorted map keys (encoding/json
// already sorts map[string]T keys) and no extra whitespace, so the same
// Snapshot value always produces the same bytes — the property a signature
// over it depends on.
func (s Snapshot) canonicalJSON() ([]byte, error) {
	return json.Marshal(s)
}
