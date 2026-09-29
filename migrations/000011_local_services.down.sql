-- Lossy: drops every local-services record, including the credit ledger.
DROP TRIGGER provider_credit_transactions_append_only ON provider_credit_transactions;
DROP FUNCTION forbid_credit_transaction_change();
DROP TABLE provider_credit_transactions;
DROP TABLE provider_topups;
DROP TABLE service_match_issues;
DROP TABLE service_matches;
ALTER TABLE service_requests DROP CONSTRAINT service_requests_selected_offer_fk;
DROP TABLE service_offers;
DROP TABLE service_request_dismissals;
DROP TABLE service_request_events;
DROP TABLE service_requests;
DROP TABLE service_providers;
