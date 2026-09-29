package localservice

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TxType is a credit ledger entry kind.
type TxType string

const (
	TxWelcomeCredit   TxType = "welcome_credit"
	TxTopUp           TxType = "top_up"
	TxMatchFee        TxType = "match_fee"
	TxRefund          TxType = "refund"
	TxAdminAdjustment TxType = "admin_adjustment"
)

// Transaction is one immutable ledger row. The provider's balance is the
// sum of its transactions; corrections are new rows, never edits.
type Transaction struct {
	ID           int64
	ProviderID   uuid.UUID
	Type         TxType
	Amount       int
	BalanceAfter int
	MatchID      *uuid.UUID
	TopupID      *uuid.UUID
	Note         *string
	CreatedAt    time.Time
}

// LedgerEntry is a ledger write the repository applies atomically with the
// balance cache. Key makes it exactly-once: the same key is never applied
// twice, however many times the operation is retried.
type LedgerEntry struct {
	ProviderID uuid.UUID
	Type       TxType
	Amount     int
	Key        string
	MatchID    *uuid.UUID
	TopupID    *uuid.UUID
	Note       *string
	At         time.Time
}

// Idempotency keys, one per economic event.
func WelcomeKey(providerID uuid.UUID) string { return "welcome:" + providerID.String() }

// MatchFeeKey is per offer: an offer can become a match at most once.
func MatchFeeKey(offerID uuid.UUID) string { return "match_fee:" + offerID.String() }
func RefundKey(matchID uuid.UUID) string   { return "refund:" + matchID.String() }
func TopUpKey(topupID uuid.UUID) string    { return "top_up:" + topupID.String() }
func AdjustmentKey(opID uuid.UUID) string  { return "admin_adjustment:" + opID.String() }

// BillingPolicy is the configurable economics of a match.
type BillingPolicy struct {
	// CreditEnabled: deduct MatchFee on each match. Off = matches are free
	// for providers but the fee they would have paid is still recorded
	// (fee_waived) to measure lead economics.
	CreditEnabled bool
	MatchFee      int
	// WelcomeCredit is granted once when a provider profile is created
	// (only while CreditEnabled).
	WelcomeCredit int
	// ConfirmTimeout: how long a selected provider has to accept.
	ConfirmTimeout time.Duration
	// RequestTTL: how long a request takes offers.
	RequestTTL time.Duration
	// RefundGrace: a customer cancelling within this long after the match,
	// before the provider set off, refunds the provider's fee.
	RefundGrace time.Duration
}

func (p BillingPolicy) Validate() error {
	switch {
	case p.MatchFee < 0 || p.MatchFee > 100_000:
		return fmt.Errorf("match fee must be 0-100000 credits")
	case p.WelcomeCredit < 0 || p.WelcomeCredit > 100_000:
		return fmt.Errorf("welcome credit must be 0-100000 credits")
	case p.ConfirmTimeout < time.Minute || p.ConfirmTimeout > 2*time.Hour:
		return fmt.Errorf("provider confirmation timeout must be between 1m and 2h")
	case p.RequestTTL < 10*time.Minute || p.RequestTTL > 7*24*time.Hour:
		return fmt.Errorf("service request TTL must be between 10m and 168h")
	case p.RefundGrace < 0 || p.RefundGrace > 24*time.Hour:
		return fmt.Errorf("refund grace must be between 0 and 24h")
	}
	return nil
}

// ChargesMatch reports whether a match deducts credit now.
func (p BillingPolicy) ChargesMatch() bool { return p.CreditEnabled && p.MatchFee > 0 }

// LowCredit reports a balance that can't pay for the next match.
func (p BillingPolicy) LowCredit(balance int) bool { return p.ChargesMatch() && balance < p.MatchFee }

// TopupStatus is a PromptPay top-up's lifecycle. Only a verified Stripe
// webhook moves it to paid (and credits the wallet); nothing the browser
// says ever does.
type TopupStatus string

const (
	TopupPending  TopupStatus = "pending"
	TopupPaid     TopupStatus = "paid"
	TopupFailed   TopupStatus = "failed"
	TopupExpired  TopupStatus = "expired"
	TopupRefunded TopupStatus = "refunded"
)

// Topup is one credit purchase, paid by scanning a PromptPay QR.
type Topup struct {
	ID                    uuid.UUID
	ProviderID            uuid.UUID
	PackageID             string
	Amount                int // minor units (satang)
	Currency              string
	CreditAmount          int
	Status                TopupStatus
	StripePaymentIntentID *string
	// PromptPayQRData is the QR payload (EMVCo string) to scan;
	// PromptPayQRImageURL is Stripe's PNG of the same QR (to save/share).
	PromptPayQRData     *string
	PromptPayQRImageURL *string
	PaidAt              *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Currency is the only top-up currency.
const Currency = "thb"

// Package is a server-defined top-up option. The client only names one by
// ID; price and credits always come from here.
type Package struct {
	ID      string
	THB     int
	Credits int
}

// AmountMinor is the Stripe amount in satang.
func (p Package) AmountMinor() int { return p.THB * 100 }

// DefaultPackages is used when PROVIDER_TOPUP_PACKAGES is unset or invalid.
var DefaultPackages = []Package{
	{ID: "starter", THB: 100, Credits: 100},
	{ID: "standard", THB: 300, Credits: 330},
	{ID: "pro", THB: 500, Credits: 575},
}

// ParsePackages reads "id:thb:credits,…". Stripe's THB minimum is 10 THB.
func ParsePackages(raw string) ([]Package, error) {
	var out []Package
	seen := map[string]bool{}
	for part := range strings.SplitSeq(raw, ",") {
		f := strings.Split(strings.TrimSpace(part), ":")
		if len(f) != 3 {
			return nil, fmt.Errorf("package %q must be id:thb:credits", part)
		}
		thb, err1 := strconv.Atoi(f[1])
		credits, err2 := strconv.Atoi(f[2])
		id := strings.TrimSpace(f[0])
		if id == "" || len(id) > 32 || err1 != nil || err2 != nil || thb < 10 || thb > 100_000 || credits < 1 || credits > 1_000_000 {
			return nil, fmt.Errorf("package %q is invalid (thb 10-100000, credits ≥ 1)", part)
		}
		if seen[id] {
			return nil, fmt.Errorf("package id %q repeats", id)
		}
		seen[id] = true
		out = append(out, Package{ID: id, THB: thb, Credits: credits})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no packages")
	}
	return out, nil
}

// FindPackage looks a package up by id.
func FindPackage(pkgs []Package, id string) (Package, bool) {
	for _, p := range pkgs {
		if p.ID == id {
			return p, true
		}
	}
	return Package{}, false
}
