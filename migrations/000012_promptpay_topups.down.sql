-- Lossy: the stored PromptPay QRs are dropped.
ALTER TABLE provider_topups DROP COLUMN promptpay_qr_image_url;
ALTER TABLE provider_topups DROP COLUMN promptpay_qr_data;
DROP INDEX idx_provider_topups_payment_intent;
CREATE INDEX idx_provider_topups_payment_intent ON provider_topups (stripe_payment_intent_id) WHERE stripe_payment_intent_id IS NOT NULL;
ALTER TABLE provider_topups ADD COLUMN stripe_checkout_session_id text UNIQUE;
