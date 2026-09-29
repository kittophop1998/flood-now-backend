// Package stripe is the Stripe adapter for provider credit top-ups: it
// creates hosted Checkout sessions and verifies/decodes webhooks. It talks
// to Stripe's REST API directly (form-encoded, bearer secret key) — two
// calls don't justify the SDK. It is only ever used for provider credit,
// never for a customer's payment for a service.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/ports"
)

// SignatureTolerance bounds how old (or early) a webhook's signed timestamp
// may be, against replay.
const SignatureTolerance = 5 * time.Minute

type Client struct {
	baseURL       string
	secretKey     string // never logged
	webhookSecret string // never logged
	http          *http.Client
}

func New(baseURL, secretKey, webhookSecret string, timeout time.Duration) *Client {
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		secretKey:     secretKey,
		webhookSecret: webhookSecret,
		http:          &http.Client{Timeout: timeout},
	}
}

// CreateCheckout creates a one-off Checkout session in THB for the top-up.
// client_reference_id and metadata carry our ids for correlation only; the
// webhook handler trusts our stored record, not these.
func (c *Client) CreateCheckout(ctx context.Context, req ports.CheckoutRequest) (*ports.CheckoutSession, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", req.SuccessURL)
	form.Set("cancel_url", req.CancelURL)
	form.Set("client_reference_id", req.TopupID.String())
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", req.Currency)
	form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(req.Amount))
	form.Set("line_items[0][price_data][product_data][name]", req.Description)
	for k, v := range map[string]string{"topup_id": req.TopupID.String(), "provider_id": req.ProviderID.String(), "package_id": req.PackageID} {
		form.Set("metadata["+k+"]", v)
		form.Set("payment_intent_data[metadata]["+k+"]", v)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build stripe request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.secretKey)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Stripe dedupes a retried create for the same top-up.
	httpReq.Header.Set("Idempotency-Key", "topup-"+req.TopupID.String())

	res, err := c.http.Do(httpReq)
	if err != nil {
		log.Printf("stripe checkout create: %v", err)
		return nil, apperr.Unavailable("the payment provider is unavailable; try again")
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		log.Printf("stripe checkout create: status %d type=%s message=%q", res.StatusCode, e.Error.Type, e.Error.Message)
		return nil, apperr.Unavailable("the payment provider is unavailable; try again")
	}
	var s struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &s); err != nil || s.ID == "" || s.URL == "" {
		return nil, fmt.Errorf("decode stripe checkout session: %v", err)
	}
	return &ports.CheckoutSession{ID: s.ID, URL: s.URL}, nil
}

// ParseWebhook verifies the Stripe-Signature header (HMAC-SHA256 of
// "<t>.<payload>" with the endpoint secret, any v1 signature, timestamp
// within SignatureTolerance) and decodes the event.
func (c *Client) ParseWebhook(payload []byte, header string, now time.Time) (*ports.PaymentEvent, error) {
	if err := VerifySignature(payload, header, c.webhookSecret, now); err != nil {
		return nil, err
	}
	var ev struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil || ev.ID == "" || ev.Type == "" {
		return nil, apperr.Validation("webhook payload is invalid", nil)
	}
	out := &ports.PaymentEvent{ID: ev.ID, Type: ev.Type, Metadata: map[string]string{}}
	switch {
	case strings.HasPrefix(ev.Type, "checkout.session."):
		var s struct {
			ID                string            `json:"id"`
			ClientReferenceID string            `json:"client_reference_id"`
			AmountTotal       int               `json:"amount_total"`
			Currency          string            `json:"currency"`
			PaymentStatus     string            `json:"payment_status"`
			PaymentIntent     *string           `json:"payment_intent"`
			Metadata          map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(ev.Data.Object, &s); err != nil {
			return nil, apperr.Validation("webhook payload is invalid", nil)
		}
		out.SessionID, out.ClientReference, out.Amount = s.ID, s.ClientReferenceID, s.AmountTotal
		out.Currency, out.PaymentStatus, out.PaymentIntentID = strings.ToLower(s.Currency), s.PaymentStatus, s.PaymentIntent
		if s.Metadata != nil {
			out.Metadata = s.Metadata
		}
	case ev.Type == "charge.refunded":
		var ch struct {
			PaymentIntent *string `json:"payment_intent"`
			Refunded      bool    `json:"refunded"`
			Currency      string  `json:"currency"`
			Amount        int     `json:"amount"`
		}
		if err := json.Unmarshal(ev.Data.Object, &ch); err != nil {
			return nil, apperr.Validation("webhook payload is invalid", nil)
		}
		out.PaymentIntentID, out.Amount, out.Currency = ch.PaymentIntent, ch.Amount, strings.ToLower(ch.Currency)
		if ch.Refunded {
			out.PaymentStatus = "refunded" // fully refunded; partial refunds are left to operators
		}
	}
	return out, nil
}

// VerifySignature checks a Stripe-Signature header against payload.
func VerifySignature(payload []byte, header, secret string, now time.Time) error {
	if secret == "" {
		return apperr.Unauthorized("webhook signature can't be verified")
	}
	var (
		ts   int64
		sigs []string
	)
	for part := range strings.SplitSeq(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return apperr.Unauthorized("webhook signature is missing")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > SignatureTolerance || d < -SignatureTolerance {
		return apperr.Unauthorized("webhook signature timestamp is outside the tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := mac.Sum(nil)
	for _, s := range sigs {
		got, err := hex.DecodeString(s)
		if err == nil && hmac.Equal(got, expected) {
			return nil
		}
	}
	return apperr.Unauthorized("webhook signature is invalid")
}

// Sign builds a Stripe-Signature header (tests and local webhook replay).
func Sign(payload []byte, secret string, at time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", at.Unix())
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(mac.Sum(nil)))
}
