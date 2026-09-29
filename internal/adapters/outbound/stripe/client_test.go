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

func TestParseWebhookPaymentIntent(t *testing.T) {
	c := New("http://unused", "sk_test", secret, time.Second)
	payload := []byte(`{"id":"evt_9","type":"payment_intent.succeeded","data":{"object":{
		"id":"pi_1","amount":30000,"amount_received":30000,"currency":"THB","status":"succeeded",
		"metadata":{"topup_id":"8e6a1d56-8b0e-4f5b-8f2c-0b7a1b6c2d3e","provider_id":"p1"}}}}`)
	ev, err := c.ParseWebhook(payload, Sign(payload, secret, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "payment_intent.succeeded" || ev.PaymentIntentID != "pi_1" || ev.Amount != 30000 || ev.Currency != "thb" ||
		ev.Status != "succeeded" || ev.TopupRef != "8e6a1d56-8b0e-4f5b-8f2c-0b7a1b6c2d3e" || ev.Metadata["provider_id"] != "p1" {
		t.Fatalf("decoded %+v", ev)
	}
	if _, err := c.ParseWebhook(payload, Sign(payload, "whsec_forged", now), now); !isUnauthorized(err) {
		t.Fatalf("forged event must not decode, got %v", err)
	}
	// A success counts what was actually received.
	short := []byte(`{"id":"evt_s","type":"payment_intent.succeeded","data":{"object":{"id":"pi_2","amount":30000,"amount_received":100,"currency":"thb","status":"succeeded"}}}`)
	if ev, _ := c.ParseWebhook(short, Sign(short, secret, now), now); ev.Amount != 100 {
		t.Fatalf("amount received = %d, want 100", ev.Amount)
	}
}

func TestParseWebhookChargeRefunded(t *testing.T) {
	c := New("http://unused", "sk_test", secret, time.Second)
	payload := []byte(`{"id":"evt_r","type":"charge.refunded","data":{"object":{"payment_intent":"pi_7","refunded":true,"amount":10000,"currency":"thb"}}}`)
	ev, err := c.ParseWebhook(payload, Sign(payload, secret, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Status != "refunded" || ev.PaymentIntentID != "pi_7" {
		t.Fatalf("decoded %+v", ev)
	}
}

func TestCreatePromptPayIsPromptPayOnlyWithServerAmount(t *testing.T) {
	var got url.Values
	var auth, idem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/payment_intents" {
			t.Errorf("path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		got, _ = url.ParseQuery(string(body))
		auth, idem = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		_, _ = w.Write([]byte(`{"id":"pi_42","status":"requires_action","next_action":{"type":"promptpay_display_qr_code",
			"promptpay_display_qr_code":{"data":"00020101021230…6304ABCD","image_url_png":"https://qr.stripe.com/x.png","hosted_instructions_url":"https://pay.stripe.com/x"}}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "sk_test_abc", secret, time.Second)
	topup := uuid.New()
	charge, err := c.CreatePromptPay(context.Background(), ports.PromptPayRequest{
		TopupID: topup, ProviderID: uuid.New(), PackageID: "standard", Amount: 30000, Currency: "thb",
		Description: "FloodNow provider credit", Email: "shop@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if charge.PaymentIntentID != "pi_42" || charge.QRData == "" || charge.QRImageURL != "https://qr.stripe.com/x.png" {
		t.Fatalf("charge %+v", charge)
	}
	if auth != "Bearer sk_test_abc" || idem != "topup-"+topup.String() {
		t.Fatalf("auth=%q idempotency=%q", auth, idem)
	}
	if got["payment_method_types[]"][0] != "promptpay" || len(got["payment_method_types[]"]) != 1 || got.Get("payment_method_data[type]") != "promptpay" ||
		got.Get("amount") != "30000" || got.Get("currency") != "thb" || got.Get("confirm") != "true" ||
		got.Get("payment_method_data[billing_details][email]") != "shop@example.com" || got.Get("metadata[topup_id]") != topup.String() {
		t.Fatalf("form %v", got)
	}
}

func TestCreatePromptPayWithoutQRIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"pi_x","status":"requires_payment_method","next_action":null}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "sk", secret, time.Second).CreatePromptPay(context.Background(), ports.PromptPayRequest{TopupID: uuid.New(), Amount: 100, Currency: "thb"})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.CodeUnavailable {
		t.Fatalf("want UPSTREAM_UNAVAILABLE, got %v", err)
	}
}

func TestCreatePromptPayUpstreamErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"payment_method_unactivated","message":"nope"}}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "sk", secret, time.Second).CreatePromptPay(context.Background(), ports.PromptPayRequest{TopupID: uuid.New(), Amount: 100, Currency: "thb"})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.CodeUnavailable {
		t.Fatalf("want UPSTREAM_UNAVAILABLE, got %v", err)
	}
}
