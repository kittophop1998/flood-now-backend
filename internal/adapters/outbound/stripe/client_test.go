package stripe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/ports"
)

const secret = "whsec_test_secret"

var now = time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

func isUnauthorized(err error) bool {
	var ae *apperr.Error
	return errors.As(err, &ae) && ae.Code == apperr.CodeUnauthorized
}

func TestVerifySignature(t *testing.T) {
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed"}`)
	good := Sign(payload, secret, now)

	if err := VerifySignature(payload, good, secret, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := VerifySignature([]byte(`{"id":"evt_1","type":"checkout.session.completed","x":1}`), good, secret, now); !isUnauthorized(err) {
		t.Fatalf("tampered payload must be rejected, got %v", err)
	}
	if err := VerifySignature(payload, good, "whsec_other", now); !isUnauthorized(err) {
		t.Fatalf("wrong secret must be rejected, got %v", err)
	}
	if err := VerifySignature(payload, good, secret, now.Add(10*time.Minute)); !isUnauthorized(err) {
		t.Fatalf("replayed old signature must be rejected, got %v", err)
	}
	if err := VerifySignature(payload, "", secret, now); !isUnauthorized(err) {
		t.Fatalf("missing header must be rejected, got %v", err)
	}
	if err := VerifySignature(payload, good, "", now); !isUnauthorized(err) {
		t.Fatalf("no configured secret must never verify, got %v", err)
	}
	// Secret rotation: Stripe sends several v1 signatures; any one may match.
	rotated := strings.Replace(good, "v1=", "v1=deadbeef,v1=", 1)
	if err := VerifySignature(payload, rotated, secret, now); err != nil {
		t.Fatalf("one valid v1 among several must verify: %v", err)
	}
}

func TestParseWebhookCheckoutSession(t *testing.T) {
	c := New("http://unused", "sk_test", secret, time.Second)
	payload := []byte(`{"id":"evt_9","type":"checkout.session.completed","data":{"object":{
		"id":"cs_test_1","client_reference_id":"8e6a1d56-8b0e-4f5b-8f2c-0b7a1b6c2d3e","amount_total":30000,"currency":"THB",
		"payment_status":"paid","payment_intent":"pi_1","metadata":{"provider_id":"p1"}}}}`)
	ev, err := c.ParseWebhook(payload, Sign(payload, secret, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "checkout.session.completed" || ev.SessionID != "cs_test_1" || ev.Amount != 30000 || ev.Currency != "thb" ||
		ev.PaymentStatus != "paid" || ev.PaymentIntentID == nil || *ev.PaymentIntentID != "pi_1" || ev.Metadata["provider_id"] != "p1" {
		t.Fatalf("decoded %+v", ev)
	}
	if _, err := c.ParseWebhook(payload, Sign(payload, "whsec_forged", now), now); !isUnauthorized(err) {
		t.Fatalf("forged event must not decode, got %v", err)
	}
}

func TestParseWebhookChargeRefunded(t *testing.T) {
	c := New("http://unused", "sk_test", secret, time.Second)
	payload := []byte(`{"id":"evt_r","type":"charge.refunded","data":{"object":{"payment_intent":"pi_7","refunded":true,"amount":10000,"currency":"thb"}}}`)
	ev, err := c.ParseWebhook(payload, Sign(payload, secret, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.PaymentStatus != "refunded" || ev.PaymentIntentID == nil || *ev.PaymentIntentID != "pi_7" {
		t.Fatalf("decoded %+v", ev)
	}
}

func TestCreateCheckoutSendsServerAmount(t *testing.T) {
	var got url.Values
	var auth, idem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got, _ = url.ParseQuery(string(body))
		auth, idem = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		_, _ = w.Write([]byte(`{"id":"cs_test_42","url":"https://checkout.stripe.com/c/pay/cs_test_42"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "sk_test_abc", secret, time.Second)
	topup := uuid.New()
	sess, err := c.CreateCheckout(context.Background(), ports.CheckoutRequest{
		TopupID: topup, ProviderID: uuid.New(), PackageID: "standard", Amount: 30000, Currency: "thb",
		Description: "FloodNow provider credit", SuccessURL: "https://app/?topup=x", CancelURL: "https://app/?topup=x&topup_cancelled=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "cs_test_42" || !strings.HasPrefix(sess.URL, "https://checkout.stripe.com/") {
		t.Fatalf("session %+v", sess)
	}
	if auth != "Bearer sk_test_abc" || idem != "topup-"+topup.String() {
		t.Fatalf("auth=%q idempotency=%q", auth, idem)
	}
	if got.Get("mode") != "payment" || got.Get("line_items[0][price_data][unit_amount]") != "30000" ||
		got.Get("line_items[0][price_data][currency]") != "thb" || got.Get("client_reference_id") != topup.String() {
		t.Fatalf("form %v", got)
	}
}

func TestCreateCheckoutUpstreamErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"nope"}}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "sk", secret, time.Second).CreateCheckout(context.Background(), ports.CheckoutRequest{TopupID: uuid.New(), Amount: 100, Currency: "thb"})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.CodeUnavailable {
		t.Fatalf("want UPSTREAM_UNAVAILABLE, got %v", err)
	}
}
