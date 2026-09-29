package localservice

import (
	"context"
	"log"
	"net/url"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	ls "floodnow-api/internal/domain/localservice"
	"floodnow-api/internal/ports"
)

// WalletView is the provider's credit wallet.
type WalletView struct {
	Provider     *ls.Provider
	Transactions []ls.Transaction
	Topups       []ls.Topup
	LowCredit    bool
}

// Wallet returns the balance (cached ledger sum), recent ledger rows and
// recent top-ups.
func (s *Service) Wallet(ctx context.Context, userID uuid.UUID) (*WalletView, error) {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, err
	}
	txs, err := s.repo.Transactions(ctx, p.ID, transactionsLimit)
	if err != nil {
		return nil, err
	}
	topups, err := s.repo.RecentTopups(ctx, p.ID, topupsLimit)
	if err != nil {
		return nil, err
	}
	return &WalletView{Provider: p, Transactions: txs, Topups: topups, LowCredit: s.cfg.Policy.LowCredit(p.CreditBalance)}, nil
}

// StartTopup creates a pending top-up for a server-defined package and a
// Stripe Checkout session for it. The client only names the package; the
// amount and credits come from the server. Nothing is credited here — only
// the verified webhook does that.
func (s *Service) StartTopup(ctx context.Context, userID uuid.UUID, packageID string) (*ls.Topup, string, error) {
	if s.cfg.Payments == nil {
		return nil, "", apperr.NotFound("credit top-ups are not available")
	}
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	pkg, ok := ls.FindPackage(s.cfg.Packages, packageID)
	if !ok {
		return nil, "", apperr.Validation("package is invalid", map[string]string{"package_id": "must be one of the offered packages"})
	}
	now := s.clock.Now()
	t := &ls.Topup{
		ID: uuid.New(), ProviderID: p.ID, PackageID: pkg.ID, Amount: pkg.AmountMinor(), Currency: ls.Currency,
		CreditAmount: pkg.Credits, Status: ls.TopupPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.CreateTopup(ctx, t); err != nil {
		return nil, "", err
	}
	sess, err := s.cfg.Payments.CreateCheckout(ctx, ports.CheckoutRequest{
		TopupID: t.ID, ProviderID: p.ID, PackageID: pkg.ID, Amount: t.Amount, Currency: t.Currency,
		Description: "FloodNow provider credit",
		SuccessURL:  s.returnURL(t.ID, false),
		CancelURL:   s.returnURL(t.ID, true),
	})
	if err != nil {
		// No session exists, so this top-up can never be paid.
		if markErr := s.repo.MarkTopup(ctx, t.ID, ls.TopupFailed, now); markErr != nil {
			log.Printf("topup %s: mark failed: %v", t.ID, markErr)
		}
		return nil, "", err
	}
	if err := s.repo.SetTopupSession(ctx, t.ID, sess.ID, now); err != nil {
		return nil, "", err
	}
	sid := sess.ID
	t.StripeCheckoutSessionID = &sid
	log.Printf("topup %s started provider=%s package=%s", t.ID, p.ID, pkg.ID)
	return t, sess.URL, nil
}

func (s *Service) returnURL(topupID uuid.UUID, cancelled bool) string {
	u, err := url.Parse(s.cfg.ReturnURL)
	if err != nil {
		return s.cfg.ReturnURL
	}
	q := u.Query()
	q.Set("topup", topupID.String())
	if cancelled {
		q.Set("topup_cancelled", "1")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Topup returns one of the provider's own top-ups (for "confirming
// payment…" polling after the Stripe redirect).
func (s *Service) Topup(ctx context.Context, userID, id uuid.UUID) (*ls.Topup, error) {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, err
	}
	t, err := s.repo.GetTopup(ctx, id)
	if err != nil {
		return nil, err
	}
	if t == nil || t.ProviderID != p.ID {
		return nil, apperr.NotFound("top-up not found")
	}
	return t, nil
}

// HandlePaymentWebhook applies a Stripe webhook. The signature is verified
// first; the credit then comes only from our own top-up record, and only
// when the event matches it (session, amount, currency). Every branch is
// idempotent and order-independent: a duplicated or late event changes
// nothing, a paid top-up is never downgraded, and credit is applied once
// (ledger key per top-up).
func (s *Service) HandlePaymentWebhook(ctx context.Context, payload []byte, signature string) error {
	if s.cfg.Payments == nil {
		return apperr.NotFound("not found")
	}
	now := s.clock.Now()
	ev, err := s.cfg.Payments.ParseWebhook(payload, signature, now)
	if err != nil {
		return err
	}
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		if ev.PaymentStatus != "paid" {
			return nil // e.g. a delayed method still processing: wait for async_payment_succeeded
		}
		t, err := s.topupFor(ctx, ev)
		if err != nil || t == nil {
			return err
		}
		if !paymentMatches(t, ev) {
			log.Printf("stripe event %s: payment does not match topup %s (amount %d %s vs %d %s) — not credited",
				ev.ID, t.ID, ev.Amount, ev.Currency, t.Amount, t.Currency)
			return nil
		}
		credited, err := s.repo.MarkTopupPaid(ctx, ports.TopupPaid{
			TopupID: t.ID, SessionID: ev.SessionID, PaymentIntentID: ev.PaymentIntentID, Amount: ev.Amount, Currency: ev.Currency, Now: now,
		})
		if err != nil {
			return err
		}
		log.Printf("stripe event %s: topup %s paid (credited=%v)", ev.ID, t.ID, credited)
	case "checkout.session.async_payment_failed", "checkout.session.expired":
		t, err := s.topupFor(ctx, ev)
		if err != nil || t == nil {
			return err
		}
		to := ls.TopupExpired
		if ev.Type == "checkout.session.async_payment_failed" {
			to = ls.TopupFailed
		}
		if err := s.repo.MarkTopup(ctx, t.ID, to, now); err != nil {
			return err
		}
		log.Printf("stripe event %s: topup %s %s", ev.ID, t.ID, to)
	case "charge.refunded":
		// An operator refunded the payment in Stripe. Record it; the credit
		// itself is corrected only by an explicit admin adjustment.
		if ev.PaymentIntentID == nil || ev.PaymentStatus != "refunded" {
			return nil
		}
		t, err := s.repo.MarkTopupRefundedByPaymentIntent(ctx, *ev.PaymentIntentID, now)
		if err != nil {
			return err
		}
		if t != nil {
			log.Printf("stripe event %s: topup %s refunded in Stripe — adjust provider %s credit manually if needed", ev.ID, t.ID, t.ProviderID)
		}
	}
	return nil
}

// topupFor finds our top-up for a checkout event: by the client reference
// we set (our id), falling back to the session id.
func (s *Service) topupFor(ctx context.Context, ev *ports.PaymentEvent) (*ls.Topup, error) {
	if id, err := uuid.Parse(ev.ClientReference); err == nil {
		t, err := s.repo.GetTopup(ctx, id)
		if err != nil || t != nil {
			return t, err
		}
	}
	if ev.SessionID != "" {
		t, err := s.repo.GetTopupBySession(ctx, ev.SessionID)
		if err != nil || t != nil {
			return t, err
		}
	}
	log.Printf("stripe event %s (%s): no matching topup", ev.ID, ev.Type)
	return nil, nil
}

// paymentMatches checks a paid event against the stored top-up. Metadata is
// only correlation: when present it must agree, but amounts come from us.
func paymentMatches(t *ls.Topup, ev *ports.PaymentEvent) bool {
	if t.StripeCheckoutSessionID != nil && *t.StripeCheckoutSessionID != ev.SessionID {
		return false
	}
	if pid, ok := ev.Metadata["provider_id"]; ok && pid != t.ProviderID.String() {
		return false
	}
	return ev.Amount == t.Amount && ev.Currency == t.Currency
}

// ---- Operator ----

func (s *Service) AdminProviders(ctx context.Context) ([]ls.Provider, error) {
	return s.repo.ListAllProviders(ctx, adminListLimit)
}

// AdminVerify sets or clears the verified badge after a real review.
func (s *Service) AdminVerify(ctx context.Context, id uuid.UUID, verified bool) error {
	if err := s.adminProvider(ctx, id); err != nil {
		return err
	}
	now := s.clock.Now()
	if verified {
		return s.repo.SetProviderVerified(ctx, id, &now, now)
	}
	return s.repo.SetProviderVerified(ctx, id, nil, now)
}

func (s *Service) AdminSetStatus(ctx context.Context, id uuid.UUID, status ls.ProviderStatus) error {
	if !status.Valid() {
		return apperr.Validation("status is invalid", map[string]string{"status": "must be active or suspended"})
	}
	if err := s.adminProvider(ctx, id); err != nil {
		return err
	}
	return s.repo.SetProviderStatus(ctx, id, status, s.clock.Now())
}

// AdminAdjust adds (or removes) credit with a reason, as a new ledger row.
func (s *Service) AdminAdjust(ctx context.Context, id uuid.UUID, amount int, note string) (*ls.Transaction, error) {
	if amount == 0 || amount < -1_000_000 || amount > 1_000_000 {
		return nil, apperr.Validation("amount is invalid", map[string]string{"amount": "must be a non-zero number of credits"})
	}
	n := ls.Trimmed(&note)
	if n == nil || len([]rune(*n)) > 500 {
		return nil, apperr.Validation("note is required", map[string]string{"note": "say why (1-500 characters)"})
	}
	if err := s.adminProvider(ctx, id); err != nil {
		return nil, err
	}
	tx, err := s.repo.AdjustCredit(ctx, ls.LedgerEntry{
		ProviderID: id, Type: ls.TxAdminAdjustment, Amount: amount, Key: ls.AdjustmentKey(uuid.New()), Note: n, At: s.clock.Now(),
	})
	if err != nil {
		return nil, err
	}
	log.Printf("admin credit adjustment provider=%s amount=%d", id, amount)
	return tx, nil
}

func (s *Service) AdminIssues(ctx context.Context) ([]ls.Issue, error) {
	return s.repo.ListIssues(ctx, adminListLimit)
}

func (s *Service) adminProvider(ctx context.Context, id uuid.UUID) error {
	p, err := s.repo.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return apperr.NotFound("service provider not found")
	}
	return nil
}
