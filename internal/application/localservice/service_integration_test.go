package localservice_test

// Integration tests for the money paths against a real, migrated Postgres:
// the guarantees (one active match, fee charged exactly once, credit only
// from a verified webhook, never twice) live in SQL transactions, so they
// are exercised there. Skipped unless FLOODNOW_TEST_DATABASE_URL points at a
// scratch database with every migration applied — never a real one:
//
//	FLOODNOW_TEST_DATABASE_URL=postgres://…/floodnow_scratch go test ./internal/application/localservice/

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/adapters/outbound/postgres"
	"floodnow-api/internal/adapters/outbound/stripe"
	appls "floodnow-api/internal/application/localservice"
	"floodnow-api/internal/domain/apperr"
	ls "floodnow-api/internal/domain/localservice"
	"floodnow-api/internal/ports"
)

const webhookSecret = "whsec_integration"

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	t     *testing.T
	db    *sql.DB
	clk   *clock
	svc   *appls.Service
	ctx   context.Context
	lat   float64
	lng   float64
	stamp string
}

func setup(t *testing.T) *fixture {
	dsn := os.Getenv("FLOODNOW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("FLOODNOW_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := postgres.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Stripe stand-in: a PromptPay PaymentIntent with its QR per call.
	stripeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ref := r.PostForm.Get("metadata[topup_id]")
		fmt.Fprintf(w, `{"id":"pi_%s","status":"requires_action","next_action":{"type":"promptpay_display_qr_code",
			"promptpay_display_qr_code":{"data":"000201-%s","image_url_png":"https://qr.stripe.test/%s.png"}}}`, ref, ref, ref)
	}))
	t.Cleanup(stripeAPI.Close)

	clk := &clock{now: time.Now().UTC().Truncate(time.Second)}
	svc := appls.NewService(postgres.NewLocalServiceRepository(db), clk, appls.Config{
		Policy: ls.BillingPolicy{
			CreditEnabled: true, MatchFee: 20, WelcomeCredit: 30,
			ConfirmTimeout: 10 * time.Minute, RequestTTL: 2 * time.Hour, RefundGrace: 10 * time.Minute,
		},
		Packages: []ls.Package{{ID: "standard", THB: 300, Credits: 330}},
		Payments: stripe.New(stripeAPI.URL, "sk_test", webhookSecret, 5*time.Second),
	})
	// A random spot so runs don't see each other's requests.
	lat := 5 + float64(time.Now().UnixNano()%1_000_000)/100_000
	return &fixture{t: t, db: db, clk: clk, svc: svc, ctx: ctx, lat: lat, lng: 100.5, stamp: uuid.NewString()[:8]}
}

func (f *fixture) user(name string) uuid.UUID {
	id := uuid.New()
	if _, err := f.db.ExecContext(f.ctx, `INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'x', $3)`,
		id, fmt.Sprintf("%s-%s@test.invalid", name, id), name); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) provider(name string, cats []ls.Category, dLat float64) (uuid.UUID, *ls.Provider) {
	owner := f.user(name)
	p, err := f.svc.SaveProvider(f.ctx, owner, ls.ProviderInput{
		DisplayName: name, Categories: cats, Phone: "08" + f.stamp, Latitude: f.lat + dLat, Longitude: f.lng,
		ServiceRadiusM: 5000, Available: true,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return owner, p
}

func (f *fixture) balance(id uuid.UUID) int {
	var b, sum int
	if err := f.db.QueryRowContext(f.ctx, `SELECT p.credit_balance, COALESCE((SELECT SUM(amount) FROM provider_credit_transactions t WHERE t.provider_id = p.id), 0)
		FROM service_providers p WHERE p.id = $1`, id).Scan(&b, &sum); err != nil {
		f.t.Fatal(err)
	}
	if b != sum {
		f.t.Fatalf("balance cache %d != ledger sum %d", b, sum)
	}
	return b
}

func (f *fixture) ledger(id uuid.UUID, typ ls.TxType) int {
	var n int
	if err := f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM provider_credit_transactions WHERE provider_id = $1 AND type = $2`, id, typ).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) request(customer uuid.UUID, cat ls.Category) *appls.RequestView {
	v, err := f.svc.CreateRequest(f.ctx, ls.NewRequestInput{CustomerUserID: customer, Category: cat, Latitude: f.lat, Longitude: f.lng, ContactPhone: "0899999999"})
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) offer(owner, requestID uuid.UUID, price int) *ls.Offer {
	o, err := f.svc.SendOffer(f.ctx, owner, requestID, ls.OfferInput{PriceTHB: &price, ETAMinutes: 20})
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

func code(err error) apperr.Code {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestMatchChargesExactlyOnceAndOnlyOnConfirmation(t *testing.T) {
	f := setup(t)
	customer := f.user("customer")
	ownerA, provA := f.provider("A", []ls.Category{ls.CatTowing}, 0.01)
	ownerB, provB := f.provider("B", []ls.Category{ls.CatTowing, ls.CatTyre}, 0.02)
	_, provFar := f.provider("Far", []ls.Category{ls.CatTowing}, 0.2)      // ~22 km: outside its 5 km radius
	_, provPlumb := f.provider("Plumber", []ls.Category{ls.CatPlumber}, 0) // wrong category
	if f.balance(provA.ID) != 30 || f.ledger(provA.ID, ls.TxWelcomeCredit) != 1 {
		t.Fatal("welcome credit should be granted once on onboarding")
	}
	// Saving the profile again never grants it twice.
	if _, err := f.svc.SaveProvider(f.ctx, ownerA, ls.ProviderInput{DisplayName: "A2", Categories: []ls.Category{ls.CatTowing}, Phone: "0811",
		Latitude: f.lat + 0.01, Longitude: f.lng, ServiceRadiusM: 5000, Available: true}); err != nil || f.balance(provA.ID) != 30 {
		t.Fatalf("re-save changed credit: %v", err)
	}

	req := f.request(customer, ls.CatTowing)

	// Discovery: category + radius + availability.
	near, err := f.svc.NearbyRequests(f.ctx, ownerA)
	if err != nil || !containsRequest(near, req.ID) {
		t.Fatalf("provider A should see the request: %v", err)
	}
	for _, p := range []*ls.Provider{provFar, provPlumb} {
		if _, err := f.svc.SendOffer(f.ctx, p.OwnerUserID, req.ID, ls.OfferInput{ETAMinutes: 10}); code(err) != apperr.CodeNotFound {
			t.Fatalf("%s must not be able to quote (out of area / category), got %v", p.DisplayName, err)
		}
	}

	// Offers are free.
	oA := f.offer(ownerA, req.ID, 800)
	oB := f.offer(ownerB, req.ID, 700)
	if f.balance(provA.ID) != 30 || f.balance(provB.ID) != 30 {
		t.Fatal("sending offers must not cost credit")
	}

	// Customer selects B: no charge.
	v, err := f.svc.SelectOffer(f.ctx, customer, req.ID, oB.ID)
	if err != nil || v.Effective != ls.StatusPendingConfirmation {
		t.Fatalf("select: %v %v", err, v)
	}
	if f.balance(provB.ID) != 30 {
		t.Fatal("the customer's selection alone must not charge the provider")
	}
	// A wasn't selected.
	if _, err := f.svc.AcceptOffer(f.ctx, ownerA, oA.ID); code(err) != apperr.CodeConflict {
		t.Fatalf("an unselected provider can't accept, got %v", err)
	}

	// B doesn't confirm in time: back to open, nothing charged, too late to accept.
	f.clk.advance(11 * time.Minute)
	if v, _ := f.svc.GetRequest(f.ctx, customer, req.ID); v.Effective != ls.StatusOpen {
		t.Fatalf("timed-out selection should reopen, got %s", v.Effective)
	}
	if _, err := f.svc.AcceptOffer(f.ctx, ownerB, oB.ID); code(err) != apperr.CodeConflict {
		t.Fatalf("accepting after the deadline must fail, got %v", err)
	}
	if f.balance(provB.ID) != 30 {
		t.Fatal("a timed-out selection must not charge")
	}
	// The timed-out offer can't be picked again; the other one can.
	if _, err := f.svc.SelectOffer(f.ctx, customer, req.ID, oB.ID); code(err) != apperr.CodeConflict {
		t.Fatalf("an expired selection can't be re-picked, got %v", err)
	}

	// Select A; A rejects → open, no charge.
	if _, err := f.svc.SelectOffer(f.ctx, customer, req.ID, oA.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RejectOffer(f.ctx, ownerA, oA.ID); err != nil {
		t.Fatal(err)
	}
	if f.balance(provA.ID) != 30 {
		t.Fatal("a rejected selection must not charge")
	}

	// A second request; A accepts from many goroutines at once.
	req2 := f.request(customer, ls.CatTowing)
	oA2 := f.offer(ownerA, req2.ID, 900)
	if _, err := f.svc.SelectOffer(f.ctx, customer, req2.ID, oA2.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.AcceptOffer(f.ctx, ownerA, oA2.ID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent/repeated accept should be idempotent, got %v", err)
		}
	}
	var matches int
	_ = f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM service_matches WHERE request_id = $1`, req2.ID).Scan(&matches)
	if matches != 1 || f.balance(provA.ID) != 10 || f.ledger(provA.ID, ls.TxMatchFee) != 1 {
		t.Fatalf("want 1 match and one 20-credit fee; matches=%d balance=%d fees=%d", matches, f.balance(provA.ID), f.ledger(provA.ID, ls.TxMatchFee))
	}

	// Contact is unlocked for the two parties only.
	cv, _ := f.svc.GetRequest(f.ctx, customer, req2.ID)
	if cv.Provider == nil || cv.Provider.Phone == "" || cv.Effective != ls.StatusMatched {
		t.Fatal("customer should see the matched provider's contact")
	}
	pv, err := f.svc.GetRequest(f.ctx, ownerA, req2.ID)
	if err != nil || pv.Role != ls.RoleProvider || pv.ContactPhone == "" {
		t.Fatalf("matched provider should see the request: %v", err)
	}
	if _, err := f.svc.GetRequest(f.ctx, ownerB, req2.ID); code(err) != apperr.CodeNotFound {
		t.Fatalf("an unmatched provider must not see a request, got %v", err)
	}

	// Customer cancels right away (before travel): fee refunded once, as a new row.
	if _, err := f.svc.Cancel(f.ctx, customer, req2.ID, ls.CancelChangedMind, nil); err != nil {
		t.Fatal(err)
	}
	if f.balance(provA.ID) != 30 || f.ledger(provA.ID, ls.TxRefund) != 1 || f.ledger(provA.ID, ls.TxMatchFee) != 1 {
		t.Fatalf("refund should restore 20 via a REFUND row; balance=%d", f.balance(provA.ID))
	}
	if _, err := f.svc.Cancel(f.ctx, customer, req2.ID, ls.CancelChangedMind, nil); code(err) != apperr.CodeConflict {
		t.Fatalf("cancelling twice must fail, got %v", err)
	}
	if f.ledger(provA.ID, ls.TxRefund) != 1 {
		t.Fatal("refund must apply once")
	}

	// The ledger is append-only.
	if _, err := f.db.ExecContext(f.ctx, `UPDATE provider_credit_transactions SET amount = 999 WHERE provider_id = $1`, provA.ID); err == nil {
		t.Fatal("ledger rows must not be editable")
	}
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM provider_credit_transactions WHERE provider_id = $1`, provA.ID); err == nil {
		t.Fatal("ledger rows must not be deletable")
	}
}

func TestInsufficientCreditThenStripeTopUp(t *testing.T) {
	f := setup(t)
	customer := f.user("customer")
	owner, prov := f.provider("Tow", []ls.Category{ls.CatTowing}, 0.01)
	// Spend the welcome credit down to 10 (< fee 20) with an operator adjustment.
	if _, err := f.svc.AdminAdjust(f.ctx, prov.ID, -20, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AdminAdjust(f.ctx, prov.ID, -11, "would go negative"); code(err) != apperr.CodeConflict {
		t.Fatalf("an adjustment below zero must be refused, got %v", err)
	}

	req := f.request(customer, ls.CatTowing)
	o := f.offer(owner, req.ID, 500)
	if _, err := f.svc.SelectOffer(f.ctx, customer, req.ID, o.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.AcceptOffer(f.ctx, owner, o.ID)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.CodeInsufficientCredit || ae.Fields["balance"] != "10" || ae.Fields["required"] != "20" {
		t.Fatalf("want INSUFFICIENT_CREDIT with balance/required, got %v", err)
	}
	if v, _ := f.svc.GetRequest(f.ctx, customer, req.ID); v.Effective != ls.StatusPendingConfirmation || v.Match != nil {
		t.Fatal("a refused accept must leave the selection pending, with no match")
	}

	// Top up via PromptPay. Showing the QR credits nothing.
	topup, err := f.svc.StartTopup(f.ctx, owner, "tow@test.invalid", "standard")
	if err != nil || topup.Amount != 30000 || topup.CreditAmount != 330 || topup.PromptPayQRData == nil || topup.StripePaymentIntentID == nil {
		t.Fatalf("start topup: %v %+v", err, topup)
	}
	if _, err := f.svc.StartTopup(f.ctx, owner, "tow@test.invalid", "free-money"); code(err) != apperr.CodeValidation {
		t.Fatalf("unknown package must be refused, got %v", err)
	}
	if f.balance(prov.ID) != 10 {
		t.Fatal("creating a PromptPay QR must not credit")
	}
	pi := *topup.StripePaymentIntentID

	// Forged signature: rejected, nothing credited.
	paid := webhook("payment_intent.succeeded", pi, topup.ID, 30000, "succeeded")
	if err := f.svc.HandlePaymentWebhook(f.ctx, paid, stripe.Sign(paid, "whsec_forged", f.clk.Now())); code(err) != apperr.CodeUnauthorized {
		t.Fatalf("forged webhook must be rejected, got %v", err)
	}
	// Wrong amount: acknowledged but not credited.
	cheap := webhook("payment_intent.succeeded", pi, topup.ID, 100, "succeeded")
	if err := f.svc.HandlePaymentWebhook(f.ctx, cheap, stripe.Sign(cheap, webhookSecret, f.clk.Now())); err != nil {
		t.Fatal(err)
	}
	// Another PaymentIntent pretending to be this top-up: nothing.
	unpaid := webhook("payment_intent.succeeded", "pi_someone_else", topup.ID, 30000, "succeeded")
	if err := f.svc.HandlePaymentWebhook(f.ctx, unpaid, stripe.Sign(unpaid, webhookSecret, f.clk.Now())); err != nil {
		t.Fatal(err)
	}
	if f.balance(prov.ID) != 10 {
		t.Fatal("mismatched/unpaid events must not credit")
	}
	// The verified payment, delivered three times.
	for range 3 {
		if err := f.svc.HandlePaymentWebhook(f.ctx, paid, stripe.Sign(paid, webhookSecret, f.clk.Now())); err != nil {
			t.Fatal(err)
		}
	}
	if f.balance(prov.ID) != 340 || f.ledger(prov.ID, ls.TxTopUp) != 1 {
		t.Fatalf("duplicate webhooks must credit once; balance=%d", f.balance(prov.ID))
	}
	// A late "canceled" never downgrades a paid top-up.
	expired := webhook("payment_intent.canceled", pi, topup.ID, 30000, "canceled")
	_ = f.svc.HandlePaymentWebhook(f.ctx, expired, stripe.Sign(expired, webhookSecret, f.clk.Now()))
	if tp, _ := f.svc.Topup(f.ctx, owner, topup.ID); tp.Status != ls.TopupPaid {
		t.Fatalf("paid top-up changed to %s", tp.Status)
	}

	// Nothing matched automatically after the top-up; the provider accepts again.
	if v, _ := f.svc.GetRequest(f.ctx, customer, req.ID); v.Match != nil {
		t.Fatal("a top-up must never match automatically")
	}
	if _, err := f.svc.AcceptOffer(f.ctx, owner, o.ID); err != nil {
		t.Fatal(err)
	}
	if f.balance(prov.ID) != 320 {
		t.Fatalf("balance after match = %d, want 320", f.balance(prov.ID))
	}

	// Job progress; after travel starts a customer cancel is not refunded.
	for _, s := range []ls.Status{ls.StatusOnTheWay} {
		if _, err := f.svc.UpdateStatus(f.ctx, owner, req.ID, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.UpdateStatus(f.ctx, customer, req.ID, ls.StatusArrived); code(err) != apperr.CodeConflict {
		t.Fatalf("only the provider marks arrival, got %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, customer, req.ID, ls.CancelNoResponse, nil); err != nil {
		t.Fatal(err)
	}
	if f.balance(prov.ID) != 320 || f.ledger(prov.ID, ls.TxRefund) != 0 {
		t.Fatal("no refund once the provider set off")
	}

	// A failed (QR timed out) then succeeded (late) payment still credits: the money was taken.
	t2, err := f.svc.StartTopup(f.ctx, owner, "tow@test.invalid", "standard")
	if err != nil {
		t.Fatal(err)
	}
	s2 := *t2.StripePaymentIntentID
	exp2 := webhook("payment_intent.payment_failed", s2, t2.ID, 30000, "requires_payment_method")
	paid2 := webhook("payment_intent.succeeded", s2, t2.ID, 30000, "succeeded")
	_ = f.svc.HandlePaymentWebhook(f.ctx, exp2, stripe.Sign(exp2, webhookSecret, f.clk.Now()))
	if err := f.svc.HandlePaymentWebhook(f.ctx, paid2, stripe.Sign(paid2, webhookSecret, f.clk.Now())); err != nil {
		t.Fatal(err)
	}
	if f.balance(prov.ID) != 650 {
		t.Fatalf("out-of-order paid event should credit; balance=%d", f.balance(prov.ID))
	}
}

func TestProviderBacksOutReopensWithoutRefund(t *testing.T) {
	f := setup(t)
	customer := f.user("customer")
	ownerA, provA := f.provider("A", []ls.Category{ls.CatBattery}, 0.01)
	ownerB, _ := f.provider("B", []ls.Category{ls.CatBattery}, 0.01)
	req := f.request(customer, ls.CatBattery)
	oA := f.offer(ownerA, req.ID, 300)
	oB := f.offer(ownerB, req.ID, 350)
	if _, err := f.svc.SelectOffer(f.ctx, customer, req.ID, oA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AcceptOffer(f.ctx, ownerA, oA.ID); err != nil {
		t.Fatal(err)
	}
	if v, err := f.svc.Cancel(f.ctx, ownerA, req.ID, ls.CancelProviderUnavailable, nil); err != nil || v != nil {
		t.Fatalf("provider back-out: %v", err)
	}
	if f.balance(provA.ID) != 10 || f.ledger(provA.ID, ls.TxRefund) != 0 {
		t.Fatal("a provider backing out keeps the fee spent")
	}
	v, err := f.svc.GetRequest(f.ctx, customer, req.ID)
	if err != nil || v.Effective != ls.StatusOpen || v.Match != nil || v.Provider != nil {
		t.Fatalf("request should reopen without contact: %v %+v", err, v)
	}
	if _, err := f.svc.GetRequest(f.ctx, ownerA, req.ID); code(err) != apperr.CodeNotFound {
		t.Fatalf("the backed-out provider loses access, got %v", err)
	}
	// The customer picks the other provider; the backed-out offer can't be picked.
	if _, err := f.svc.SelectOffer(f.ctx, customer, req.ID, oA.ID); code(err) != apperr.CodeConflict {
		t.Fatalf("backed-out offer must not be selectable, got %v", err)
	}
	if _, err := f.svc.SelectOffer(f.ctx, customer, req.ID, oB.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AcceptOffer(f.ctx, ownerB, oB.ID); err != nil {
		t.Fatalf("a second provider can match after the first backed out: %v", err)
	}
	for _, s := range []ls.Status{ls.StatusOnTheWay, ls.StatusArrived, ls.StatusCompleted} {
		if _, err := f.svc.UpdateStatus(f.ctx, ownerB, req.ID, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := f.svc.ReportIssue(f.ctx, customer, req.ID, "price_dispute", nil); err != nil {
		t.Fatal(err)
	}
}

func containsRequest(items []ports.RequestWithDistance, id uuid.UUID) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

func webhook(typ, paymentIntent string, topupID uuid.UUID, amount int, status string) []byte {
	return fmt.Appendf(nil, `{"id":"evt_%s","type":%q,"data":{"object":{"id":%q,"amount":%d,"amount_received":%d,"currency":"thb","status":%q,"metadata":{"topup_id":%q}}}}`,
		uuid.NewString(), typ, paymentIntent, amount, amount, status, topupID)
}
