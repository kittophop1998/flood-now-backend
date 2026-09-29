-- Credit top-ups move from Stripe Checkout to PromptPay only: the API
-- creates a Stripe PaymentIntent (payment_method_types = promptpay) and the
-- app shows its QR. Checkout sessions are no longer created, so their
-- column goes; the PaymentIntent becomes the unique Stripe reference.
-- Rows already stored keep their status and ledger entries.
ALTER TABLE provider_topups DROP COLUMN stripe_checkout_session_id;
DROP INDEX idx_provider_topups_payment_intent;
CREATE UNIQUE INDEX idx_provider_topups_payment_intent ON provider_topups (stripe_payment_intent_id) WHERE stripe_payment_intent_id IS NOT NULL;
-- The PromptPay QR payload (EMVCo string) and Stripe's PNG of it, kept so
-- an unpaid QR can be shown again. A payment request, not card data.
ALTER TABLE provider_topups ADD COLUMN promptpay_qr_data text;
ALTER TABLE provider_topups ADD COLUMN promptpay_qr_image_url text;
