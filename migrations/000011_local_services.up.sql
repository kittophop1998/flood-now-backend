-- Local services: commercial service providers (shops, mechanics, towing…),
-- customer service requests, provider offers, qualified matches and the
-- provider credit wallet with Stripe top-ups. See docs/database.md.
--
-- A separate domain from community SOS / volunteer helpers: nothing here
-- reads or writes sos_requests or helpers, and no existing row changes.

-- One provider profile per user account (a user may independently also be
-- a volunteer helper — that stays in `helpers`, keyed by device).
CREATE TABLE service_providers (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Financial history hangs off the provider: never cascade-delete it.
    owner_user_id       uuid NOT NULL UNIQUE REFERENCES users (id) ON DELETE RESTRICT,
    display_name        text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 80),
    categories          text[] NOT NULL CHECK (
        cardinality(categories) BETWEEN 1 AND 10 AND
        categories <@ ARRAY['towing', 'auto_repair', 'tyre', 'battery', 'mobile_mechanic',
                            'transport', 'electrician', 'plumber', 'water_pump', 'other']::text[]),
    description         text CHECK (description IS NULL OR length(description) <= 2000),
    phone               text NOT NULL CHECK (length(phone) BETWEEN 3 AND 32),
    line_id             text CHECK (line_id IS NULL OR length(line_id) <= 64),
    latitude            double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude           double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    location_name       text CHECK (location_name IS NULL OR length(location_name) <= 200),
    service_radius_m    integer NOT NULL CHECK (service_radius_m IN (3000, 5000, 10000, 20000, 50000)),
    business_hours      text CHECK (business_hours IS NULL OR length(business_hours) <= 200),
    mobile_service      boolean NOT NULL DEFAULT false,
    available           boolean NOT NULL DEFAULT false,
    starting_price_thb  integer CHECK (starting_price_thb IS NULL OR starting_price_thb BETWEEN 0 AND 1000000),
    logo_key            text,
    -- Set only by an operator after a real review; never purchasable.
    verified_at         timestamptz,
    status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    -- Cache of SUM(provider_credit_transactions.amount); updated in the same
    -- transaction as every ledger insert. The ledger is the source of truth.
    credit_balance      integer NOT NULL DEFAULT 0 CHECK (credit_balance >= 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
-- Customer browse: bbox prefilter over active providers.
CREATE INDEX idx_service_providers_lat_lng ON service_providers (latitude, longitude) WHERE status = 'active';

-- Customer service requests. The exact point and contact phone are private
-- until a match (only the customer and the matched provider read them).
CREATE TABLE service_requests (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_user_id      uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    client_id             text CHECK (client_id IS NULL OR length(client_id) BETWEEN 8 AND 64),
    category              text NOT NULL CHECK (category IN ('towing', 'auto_repair', 'tyre', 'battery', 'mobile_mechanic',
                                                            'transport', 'electrician', 'plumber', 'water_pump', 'other')),
    description           text CHECK (description IS NULL OR length(description) <= 1000),
    vehicle_info          text CHECK (vehicle_info IS NULL OR length(vehicle_info) <= 120),
    image_key             text,
    latitude              double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude             double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    location_name         text CHECK (location_name IS NULL OR length(location_name) <= 200),
    contact_phone         text NOT NULL CHECK (length(contact_phone) BETWEEN 3 AND 32),
    status                text NOT NULL CHECK (status IN ('open', 'pending_provider_confirmation', 'matched',
                                                          'on_the_way', 'arrived', 'completed', 'cancelled', 'expired')),
    -- The offer the customer picked, awaiting the provider's confirmation
    -- until selection_expires_at.
    selected_offer_id     uuid,
    selection_expires_at  timestamptz,
    expires_at            timestamptz NOT NULL,
    cancel_reason         text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    closed_at             timestamptz,
    CHECK ((status = 'pending_provider_confirmation') = (selected_offer_id IS NOT NULL AND selection_expires_at IS NOT NULL))
);
CREATE UNIQUE INDEX idx_service_requests_client ON service_requests (customer_user_id, client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_service_requests_customer ON service_requests (customer_user_id, created_at DESC);
-- Provider discovery: bbox prefilter over requests still taking offers.
CREATE INDEX idx_service_requests_open_lat_lng ON service_requests (latitude, longitude)
    INCLUDE (category, expires_at) WHERE status IN ('open', 'pending_provider_confirmation');

-- Timeline/audit of every request transition (like sos_events).
CREATE TABLE service_request_events (
    id          bigserial PRIMARY KEY,
    request_id  uuid NOT NULL REFERENCES service_requests (id) ON DELETE CASCADE,
    status      text NOT NULL,
    actor       text NOT NULL CHECK (actor IN ('customer', 'provider', 'system')),
    note        text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_service_request_events_request ON service_request_events (request_id, created_at);

-- A provider hid a request from their list ("ignore"). Costs nothing.
CREATE TABLE service_request_dismissals (
    request_id   uuid NOT NULL REFERENCES service_requests (id) ON DELETE CASCADE,
    provider_id  uuid NOT NULL REFERENCES service_providers (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_id, request_id)
);

-- Provider offers. Sending one is free. One offer per provider per request
-- (editable while still pending).
CREATE TABLE service_offers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id   uuid NOT NULL REFERENCES service_requests (id) ON DELETE CASCADE,
    provider_id  uuid NOT NULL REFERENCES service_providers (id) ON DELETE RESTRICT,
    -- NULL = "ประเมินหน้างาน" (price assessed on site).
    price_thb    integer CHECK (price_thb IS NULL OR price_thb BETWEEN 0 AND 1000000),
    eta_minutes  integer NOT NULL CHECK (eta_minutes BETWEEN 1 AND 1440),
    note         text CHECK (note IS NULL OR length(note) <= 500),
    status       text NOT NULL CHECK (status IN ('pending', 'selected', 'accepted', 'rejected', 'expired')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (request_id, provider_id)
);
CREATE INDEX idx_service_offers_provider ON service_offers (provider_id, updated_at DESC);
ALTER TABLE service_requests ADD CONSTRAINT service_requests_selected_offer_fk
    FOREIGN KEY (selected_offer_id) REFERENCES service_offers (id);

-- A qualified match: the customer selected the offer AND the provider
-- accepted it. This is the (only) billable event.
CREATE TABLE service_matches (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id         uuid NOT NULL REFERENCES service_requests (id) ON DELETE RESTRICT,
    offer_id           uuid NOT NULL UNIQUE REFERENCES service_offers (id) ON DELETE RESTRICT,
    provider_id        uuid NOT NULL REFERENCES service_providers (id) ON DELETE RESTRICT,
    -- The match fee in credits at the time of the match; when billing is
    -- waived it is still recorded (fee_waived) but nothing is deducted.
    fee_credits        integer NOT NULL CHECK (fee_credits >= 0),
    fee_waived         boolean NOT NULL,
    status             text NOT NULL CHECK (status IN ('active', 'completed', 'cancelled')),
    cancelled_by       text CHECK (cancelled_by IS NULL OR cancelled_by IN ('customer', 'provider')),
    cancel_reason      text,
    started_travel_at  timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    closed_at          timestamptz
);
-- Only one active match per request, enforced by the database.
CREATE UNIQUE INDEX idx_service_matches_one_active ON service_matches (request_id) WHERE status = 'active';
CREATE INDEX idx_service_matches_provider ON service_matches (provider_id, created_at DESC);

-- "Couldn't reach them / no-show" reports on a match. Recorded for
-- operators; no automated dispute handling.
CREATE TABLE service_match_issues (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id     uuid NOT NULL REFERENCES service_matches (id) ON DELETE CASCADE,
    reporter     text NOT NULL CHECK (reporter IN ('customer', 'provider')),
    reason       text NOT NULL CHECK (reason IN ('no_response', 'unable_to_contact', 'no_show', 'price_dispute', 'safety', 'other')),
    details      text CHECK (details IS NULL OR length(details) <= 1000),
    status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_service_match_issues_created ON service_match_issues (created_at DESC);

-- Stripe credit top-ups. Amount/credits come from the server-side package
-- table, never from the client. Credited only by a verified webhook.
CREATE TABLE provider_topups (
    id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id                 uuid NOT NULL REFERENCES service_providers (id) ON DELETE RESTRICT,
    package_id                  text NOT NULL,
    -- Minor units (satang for THB), as Stripe counts them.
    amount                      integer NOT NULL CHECK (amount > 0),
    currency                    text NOT NULL CHECK (currency = 'thb'),
    credit_amount               integer NOT NULL CHECK (credit_amount > 0),
    status                      text NOT NULL CHECK (status IN ('pending', 'paid', 'failed', 'expired', 'refunded')),
    stripe_checkout_session_id  text UNIQUE,
    stripe_payment_intent_id    text,
    paid_at                     timestamptz,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_provider_topups_provider ON provider_topups (provider_id, created_at DESC);
CREATE INDEX idx_provider_topups_payment_intent ON provider_topups (stripe_payment_intent_id) WHERE stripe_payment_intent_id IS NOT NULL;

-- Append-only credit ledger: the source of truth for a provider's balance.
-- Every economic event has one idempotency key, so a retried API call or a
-- duplicated webhook can never apply twice.
CREATE TABLE provider_credit_transactions (
    id               bigserial PRIMARY KEY,
    provider_id      uuid NOT NULL REFERENCES service_providers (id) ON DELETE RESTRICT,
    type             text NOT NULL CHECK (type IN ('welcome_credit', 'top_up', 'match_fee', 'refund', 'admin_adjustment')),
    amount           integer NOT NULL CHECK (amount <> 0),
    balance_after    integer NOT NULL CHECK (balance_after >= 0),
    idempotency_key  text NOT NULL UNIQUE,
    match_id         uuid REFERENCES service_matches (id) ON DELETE RESTRICT,
    topup_id         uuid REFERENCES provider_topups (id) ON DELETE RESTRICT,
    note             text CHECK (note IS NULL OR length(note) <= 500),
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (type IN ('welcome_credit', 'top_up', 'refund') AND amount > 0) OR
        (type = 'match_fee' AND amount < 0) OR
        type = 'admin_adjustment'
    ),
    CHECK ((type = 'top_up') = (topup_id IS NOT NULL)),
    CHECK ((type IN ('match_fee', 'refund')) = (match_id IS NOT NULL))
);
CREATE INDEX idx_provider_credit_transactions_provider ON provider_credit_transactions (provider_id, created_at DESC, id DESC);

-- Immutable: a correction is a new row (refund / admin adjustment), never
-- an edit or a delete of an old one.
CREATE FUNCTION forbid_credit_transaction_change() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'provider_credit_transactions is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER provider_credit_transactions_append_only
    BEFORE UPDATE OR DELETE ON provider_credit_transactions
    FOR EACH ROW EXECUTE FUNCTION forbid_credit_transaction_change();
