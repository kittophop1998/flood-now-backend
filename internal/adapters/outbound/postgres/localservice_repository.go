package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	ls "floodnow-api/internal/domain/localservice"
	"floodnow-api/internal/ports"
)

// LocalServiceRepository stores providers, service requests, offers,
// matches and the provider credit ledger. Every write that must not race
// (select, accept + fee, cancel + refund, top-up credit) is one transaction
// with the request (and, for credit, the provider) row locked. The ledger is
// append-only; credit_balance is its cache, changed only together with a
// ledger insert.
type LocalServiceRepository struct {
	db *sql.DB
}

func NewLocalServiceRepository(db *sql.DB) *LocalServiceRepository {
	return &LocalServiceRepository{db: db}
}

// ---- scanning ----

const providerColumns = `p.id, p.owner_user_id, p.display_name, array_to_string(p.categories, ','), p.description, p.phone, p.line_id,
	p.latitude, p.longitude, p.location_name, p.service_radius_m, p.business_hours, p.mobile_service, p.available,
	p.starting_price_thb, p.logo_key, p.verified_at, p.status, p.credit_balance, p.created_at, p.updated_at`

func scanProvider(row rowScanner, extra ...any) (*ls.Provider, error) {
	var (
		p    ls.Provider
		cats string
	)
	dest := []any{&p.ID, &p.OwnerUserID, &p.DisplayName, &cats, &p.Description, &p.Phone, &p.LineID,
		&p.Latitude, &p.Longitude, &p.LocationName, &p.ServiceRadiusM, &p.BusinessHours, &p.MobileService, &p.Available,
		&p.StartingPriceTHB, &p.LogoKey, &p.VerifiedAt, &p.Status, &p.CreditBalance, &p.CreatedAt, &p.UpdatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	p.Categories = []ls.Category{}
	for _, c := range strings.Split(cats, ",") {
		if c != "" {
			p.Categories = append(p.Categories, ls.Category(c))
		}
	}
	return &p, nil
}

func categoryStrings(cs []ls.Category) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

const requestColumns = `r.id, r.customer_user_id, r.client_id, r.category, r.description, r.vehicle_info, r.image_key,
	r.latitude, r.longitude, r.location_name, r.contact_phone, r.status, r.selected_offer_id, r.selection_expires_at,
	r.expires_at, r.cancel_reason, r.created_at, r.updated_at, r.closed_at`

func scanRequest(row rowScanner, extra ...any) (*ls.Request, error) {
	var r ls.Request
	dest := []any{&r.ID, &r.CustomerUserID, &r.ClientID, &r.Category, &r.Description, &r.VehicleInfo, &r.ImageKey,
		&r.Latitude, &r.Longitude, &r.LocationName, &r.ContactPhone, &r.Status, &r.SelectedOfferID, &r.SelectionExpiresAt,
		&r.ExpiresAt, &r.CancelReason, &r.CreatedAt, &r.UpdatedAt, &r.ClosedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	return &r, nil
}

const offerColumns = `o.id, o.request_id, o.provider_id, o.price_thb, o.eta_minutes, o.note, o.status, o.created_at, o.updated_at`

func offerDest(o *ls.Offer) []any {
	return []any{&o.ID, &o.RequestID, &o.ProviderID, &o.PriceTHB, &o.ETAMinutes, &o.Note, &o.Status, &o.CreatedAt, &o.UpdatedAt}
}

const matchColumns = `m.id, m.request_id, m.offer_id, m.provider_id, m.fee_credits, m.fee_waived, m.status, m.cancelled_by,
	m.cancel_reason, m.started_travel_at, m.created_at, m.closed_at`

func matchDest(m *ls.Match) []any {
	return []any{&m.ID, &m.RequestID, &m.OfferID, &m.ProviderID, &m.FeeCredits, &m.FeeWaived, &m.Status, &m.CancelledBy,
		&m.CancelReason, &m.StartedTravelAt, &m.CreatedAt, &m.ClosedAt}
}

const topupColumns = `t.id, t.provider_id, t.package_id, t.amount, t.currency, t.credit_amount, t.status,
	t.stripe_checkout_session_id, t.stripe_payment_intent_id, t.paid_at, t.created_at, t.updated_at`

func scanTopup(row rowScanner) (*ls.Topup, error) {
	var t ls.Topup
	if err := row.Scan(&t.ID, &t.ProviderID, &t.PackageID, &t.Amount, &t.Currency, &t.CreditAmount, &t.Status,
		&t.StripeCheckoutSessionID, &t.StripePaymentIntentID, &t.PaidAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func noRows[T any](v *T, err error, what string) (*T, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return v, nil
}

// haversineSQL is the distance in meters between (latCol, lngCol) and a point.
func haversineSQL(latCol, lngCol, latPH, lngPH string) string {
	return fmt.Sprintf(`(2 * 6371000 * asin(sqrt(
		power(sin(radians(%[1]s - %[3]s) / 2), 2) +
		cos(radians(%[3]s)) * cos(radians(%[1]s)) * power(sin(radians(%[2]s - %[4]s) / 2), 2)
	)))`, latCol, lngCol, latPH, lngPH)
}

// ---- providers ----

func (repo *LocalServiceRepository) GetProvider(ctx context.Context, id uuid.UUID) (*ls.Provider, error) {
	p, err := scanProvider(repo.db.QueryRowContext(ctx, `SELECT `+providerColumns+` FROM service_providers p WHERE p.id = $1`, id))
	return noRows(p, err, "get provider")
}

func (repo *LocalServiceRepository) GetProviderByOwner(ctx context.Context, userID uuid.UUID) (*ls.Provider, error) {
	p, err := scanProvider(repo.db.QueryRowContext(ctx, `SELECT `+providerColumns+` FROM service_providers p WHERE p.owner_user_id = $1`, userID))
	return noRows(p, err, "get provider by owner")
}

func (repo *LocalServiceRepository) CreateProvider(ctx context.Context, p *ls.Provider, welcome *ls.LedgerEntry) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO service_providers (id, owner_user_id, display_name, categories, description, phone, line_id,
			latitude, longitude, location_name, service_radius_m, business_hours, mobile_service, available,
			starting_price_thb, logo_key, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)`,
		p.ID, p.OwnerUserID, p.DisplayName, categoryStrings(p.Categories), p.Description, p.Phone, p.LineID,
		p.Latitude, p.Longitude, p.LocationName, p.ServiceRadiusM, p.BusinessHours, p.MobileService, p.Available,
		p.StartingPriceTHB, p.LogoKey, p.Status, p.CreatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return apperr.Conflict("you already have a service provider profile")
		}
		return fmt.Errorf("insert provider: %w", err)
	}
	if welcome != nil {
		if _, err := applyLedger(ctx, tx, *welcome); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (repo *LocalServiceRepository) UpdateProvider(ctx context.Context, p *ls.Provider) error {
	if _, err := repo.db.ExecContext(ctx, `
		UPDATE service_providers SET display_name = $2, categories = $3, description = $4, phone = $5, line_id = $6,
			latitude = $7, longitude = $8, location_name = $9, service_radius_m = $10, business_hours = $11,
			mobile_service = $12, available = $13, starting_price_thb = $14, logo_key = $15, updated_at = $16
		WHERE id = $1`,
		p.ID, p.DisplayName, categoryStrings(p.Categories), p.Description, p.Phone, p.LineID,
		p.Latitude, p.Longitude, p.LocationName, p.ServiceRadiusM, p.BusinessHours,
		p.MobileService, p.Available, p.StartingPriceTHB, p.LogoKey, p.UpdatedAt,
	); err != nil {
		return fmt.Errorf("update provider: %w", err)
	}
	return nil
}

func (repo *LocalServiceRepository) SetProviderAvailability(ctx context.Context, id uuid.UUID, available bool, now time.Time) error {
	if _, err := repo.db.ExecContext(ctx, `UPDATE service_providers SET available = $2, updated_at = $3 WHERE id = $1`, id, available, now); err != nil {
		return fmt.Errorf("set provider availability: %w", err)
	}
	return nil
}

func (repo *LocalServiceRepository) ListProviders(ctx context.Context, q ports.ProviderQuery) ([]ports.ProviderWithDistance, error) {
	var b queryBuilder
	b.add("p.status = 'active'")
	b.add(b.bboxSQL("p.latitude", "p.longitude", radiusBox(q.Latitude, q.Longitude, q.RadiusM)))
	if q.Category != nil {
		b.add(b.arg(string(*q.Category)) + " = ANY(p.categories)")
	}
	if q.AvailableOnly {
		b.add("p.available")
	}
	if q.Text != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Text) + "%"
		ph := b.arg(like)
		b.add("(p.display_name ILIKE " + ph + " OR p.description ILIKE " + ph + ")")
	}
	dist := haversineSQL("p.latitude", "p.longitude", b.arg(q.Latitude), b.arg(q.Longitude))
	b.add(dist + " <= " + b.arg(q.RadiusM))
	rows, err := repo.db.QueryContext(ctx, `SELECT `+providerColumns+`, `+dist+` AS distance_m FROM service_providers p`+b.whereSQL()+
		` ORDER BY p.available DESC, distance_m LIMIT `+b.arg(q.Limit), b.args...)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	out := []ports.ProviderWithDistance{}
	for rows.Next() {
		var item ports.ProviderWithDistance
		p, err := scanProvider(rows, &item.DistanceM)
		if err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		item.Provider = *p
		out = append(out, item)
	}
	return out, rows.Err()
}

// ---- requests ----

func insertRequestEvent(ctx context.Context, tx *sql.Tx, id uuid.UUID, status ls.Status, actor ls.Role, note *string, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO service_request_events (request_id, status, actor, note, created_at) VALUES ($1, $2, $3, $4, $5)`,
		id, status, actor, note, at); err != nil {
		return fmt.Errorf("insert service request event: %w", err)
	}
	return nil
}

func (repo *LocalServiceRepository) CreateRequest(ctx context.Context, r *ls.Request) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO service_requests (id, customer_user_id, client_id, category, description, vehicle_info, image_key,
			latitude, longitude, location_name, contact_phone, status, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`,
		r.ID, r.CustomerUserID, r.ClientID, r.Category, r.Description, r.VehicleInfo, r.ImageKey,
		r.Latitude, r.Longitude, r.LocationName, r.ContactPhone, r.Status, r.ExpiresAt, r.CreatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return apperr.Conflict("this request was already sent")
		}
		return fmt.Errorf("insert service request: %w", err)
	}
	if err := insertRequestEvent(ctx, tx, r.ID, r.Status, ls.RoleCustomer, nil, r.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (repo *LocalServiceRepository) GetRequest(ctx context.Context, id uuid.UUID) (*ls.Request, error) {
	r, err := scanRequest(repo.db.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM service_requests r WHERE r.id = $1`, id))
	return noRows(r, err, "get service request")
}

func (repo *LocalServiceRepository) FindRequestByClientID(ctx context.Context, customerID uuid.UUID, clientID string) (*ls.Request, error) {
	r, err := scanRequest(repo.db.QueryRowContext(ctx,
		`SELECT `+requestColumns+` FROM service_requests r WHERE r.customer_user_id = $1 AND r.client_id = $2`, customerID, clientID))
	return noRows(r, err, "find service request by client id")
}

func (repo *LocalServiceRepository) CountOpenRequests(ctx context.Context, customerID uuid.UUID, now time.Time) (int, error) {
	var n int
	if err := repo.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM service_requests
		WHERE customer_user_id = $1 AND (
			(status IN ('open', 'pending_provider_confirmation') AND expires_at > $2) OR
			status IN ('matched', 'on_the_way', 'arrived'))`, customerID, now).Scan(&n); err != nil {
		return 0, fmt.Errorf("count open service requests: %w", err)
	}
	return n, nil
}

func (repo *LocalServiceRepository) ListRequestsByCustomer(ctx context.Context, customerID uuid.UUID, limit int) ([]ls.Request, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT `+requestColumns+` FROM service_requests r WHERE r.customer_user_id = $1
		ORDER BY r.created_at DESC LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list service requests: %w", err)
	}
	defer rows.Close()
	out := []ls.Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan service request: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (repo *LocalServiceRepository) RequestEvents(ctx context.Context, id uuid.UUID) ([]ls.Event, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT status, actor, note, created_at FROM service_request_events
		WHERE request_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("query service request events: %w", err)
	}
	defer rows.Close()
	out := []ls.Event{}
	for rows.Next() {
		var e ls.Event
		if err := rows.Scan(&e.Status, &e.Actor, &e.Note, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan service request event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (repo *LocalServiceRepository) OpenRequestsNear(ctx context.Context, q ports.OpenRequestQuery) ([]ports.RequestWithDistance, error) {
	if len(q.Categories) == 0 {
		return []ports.RequestWithDistance{}, nil
	}
	var b queryBuilder
	b.add("r.status IN ('open', 'pending_provider_confirmation')")
	b.add("r.expires_at > " + b.arg(q.Now))
	b.add(b.bboxSQL("r.latitude", "r.longitude", radiusBox(q.Latitude, q.Longitude, q.RadiusM)))
	b.inList("r.category", categoryStrings(q.Categories))
	b.add("r.customer_user_id <> " + b.arg(q.ExcludeCustomerID))
	provider := b.arg(q.ProviderID)
	b.add("NOT EXISTS (SELECT 1 FROM service_request_dismissals d WHERE d.provider_id = " + provider + " AND d.request_id = r.id)")
	dist := haversineSQL("r.latitude", "r.longitude", b.arg(q.Latitude), b.arg(q.Longitude))
	b.add(dist + " <= " + b.arg(q.RadiusM))
	rows, err := repo.db.QueryContext(ctx, `SELECT `+requestColumns+`, `+dist+` AS distance_m, `+offerColumns+`
		FROM service_requests r
		LEFT JOIN service_offers o ON o.request_id = r.id AND o.provider_id = `+provider+b.whereSQL()+
		` ORDER BY r.created_at DESC LIMIT `+b.arg(q.Limit), b.args...)
	if err != nil {
		return nil, fmt.Errorf("query open service requests: %w", err)
	}
	defer rows.Close()
	out := []ports.RequestWithDistance{}
	for rows.Next() {
		var (
			item ports.RequestWithDistance
			o    nullOffer
		)
		r, err := scanRequest(rows, append([]any{&item.DistanceM}, o.dest()...)...)
		if err != nil {
			return nil, fmt.Errorf("scan open service request: %w", err)
		}
		item.Request = *r
		item.MyOffer = o.offer()
		out = append(out, item)
	}
	return out, rows.Err()
}

// nullOffer scans an offer from a LEFT JOIN (all columns NULL when absent).
type nullOffer struct {
	id, requestID, providerID uuid.NullUUID
	price                     sql.NullInt64
	eta                       sql.NullInt64
	note                      sql.NullString
	status                    sql.NullString
	createdAt, updatedAt      sql.NullTime
}

func (n *nullOffer) dest() []any {
	return []any{&n.id, &n.requestID, &n.providerID, &n.price, &n.eta, &n.note, &n.status, &n.createdAt, &n.updatedAt}
}

func (n *nullOffer) offer() *ls.Offer {
	if !n.id.Valid {
		return nil
	}
	o := &ls.Offer{ID: n.id.UUID, RequestID: n.requestID.UUID, ProviderID: n.providerID.UUID, ETAMinutes: int(n.eta.Int64),
		Status: ls.OfferStatus(n.status.String), CreatedAt: n.createdAt.Time, UpdatedAt: n.updatedAt.Time}
	if n.price.Valid {
		p := int(n.price.Int64)
		o.PriceTHB = &p
	}
	if n.note.Valid {
		o.Note = &n.note.String
	}
	return o
}

func (repo *LocalServiceRepository) DismissRequest(ctx context.Context, requestID, providerID uuid.UUID, now time.Time) error {
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO service_request_dismissals (request_id, provider_id, created_at)
		VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, requestID, providerID, now); err != nil {
		return fmt.Errorf("dismiss service request: %w", err)
	}
	return nil
}

// ---- offers ----

func (repo *LocalServiceRepository) UpsertOffer(ctx context.Context, o *ls.Offer, now time.Time) (bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	r, err := lockRequest(ctx, tx, o.RequestID, now)
	if err != nil || r == nil {
		return false, err
	}
	if !r.Status.TakesOffers() {
		return false, tx.Commit() // commit any normalization
	}
	// A new offer, or an edit of this provider's still-pending one; a
	// selected/accepted/rejected offer is left alone (0 rows → false).
	row := tx.QueryRowContext(ctx, `
		INSERT INTO service_offers (id, request_id, provider_id, price_thb, eta_minutes, note, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $7)
		ON CONFLICT (request_id, provider_id) DO UPDATE SET
			price_thb = EXCLUDED.price_thb, eta_minutes = EXCLUDED.eta_minutes, note = EXCLUDED.note, updated_at = EXCLUDED.updated_at
		WHERE service_offers.status = 'pending'
		RETURNING id, created_at`,
		o.ID, o.RequestID, o.ProviderID, o.PriceTHB, o.ETAMinutes, o.Note, now)
	if err := row.Scan(&o.ID, &o.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("upsert offer: %w", err)
	}
	return true, tx.Commit()
}

func (repo *LocalServiceRepository) GetOffer(ctx context.Context, id uuid.UUID) (*ls.Offer, error) {
	var o ls.Offer
	err := repo.db.QueryRowContext(ctx, `SELECT `+offerColumns+` FROM service_offers o WHERE o.id = $1`, id).Scan(offerDest(&o)...)
	return noRows(&o, err, "get offer")
}

func (repo *LocalServiceRepository) OffersForRequest(ctx context.Context, requestID uuid.UUID) ([]ports.OfferWithProvider, error) {
	dist := haversineSQL("p.latitude", "p.longitude", "r.latitude", "r.longitude")
	rows, err := repo.db.QueryContext(ctx, `SELECT `+offerColumns+`, `+providerColumns+`, `+dist+`
		FROM service_offers o
		JOIN service_providers p ON p.id = o.provider_id
		JOIN service_requests r ON r.id = o.request_id
		WHERE o.request_id = $1
		ORDER BY (o.status IN ('pending', 'selected', 'accepted')) DESC, o.price_thb NULLS LAST, o.eta_minutes, o.created_at`, requestID)
	if err != nil {
		return nil, fmt.Errorf("list offers: %w", err)
	}
	defer rows.Close()
	out := []ports.OfferWithProvider{}
	for rows.Next() {
		var item ports.OfferWithProvider
		p, err := scanProvider(multiScanner{rows: rows, before: offerDest(&item.Offer)}, &item.DistanceM)
		if err != nil {
			return nil, fmt.Errorf("scan offer: %w", err)
		}
		item.Provider = *p
		out = append(out, item)
	}
	return out, rows.Err()
}

func (repo *LocalServiceRepository) OffersByProvider(ctx context.Context, providerID uuid.UUID, limit int) ([]ports.OfferWithRequest, error) {
	dist := haversineSQL("r.latitude", "r.longitude", "p.latitude", "p.longitude")
	rows, err := repo.db.QueryContext(ctx, `SELECT `+offerColumns+`, `+requestColumns+`, `+dist+`
		FROM service_offers o
		JOIN service_requests r ON r.id = o.request_id
		JOIN service_providers p ON p.id = o.provider_id
		WHERE o.provider_id = $1
		ORDER BY o.updated_at DESC LIMIT $2`, providerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list provider offers: %w", err)
	}
	defer rows.Close()
	out := []ports.OfferWithRequest{}
	for rows.Next() {
		var item ports.OfferWithRequest
		dest := offerDest(&item.Offer)
		r, err := scanRequest(multiScanner{rows: rows, before: dest}, &item.DistanceM)
		if err != nil {
			return nil, fmt.Errorf("scan provider offer: %w", err)
		}
		item.Request = *r
		out = append(out, item)
	}
	return out, rows.Err()
}

// multiScanner lets a scan helper for a later column group run after some
// leading columns: Scan(dest...) scans before+dest in one call.
type multiScanner struct {
	rows   rowScanner
	before []any
}

func (m multiScanner) Scan(dest ...any) error { return m.rows.Scan(append(m.before, dest...)...) }

// ---- matching ----

// lockRequest locks the request row and brings it up to date first: a
// pending selection past its deadline returns to open (its offer expires),
// and a request past expiry that never matched becomes expired. The rule
// itself is the domain's (Request.Effective).
func lockRequest(ctx context.Context, tx *sql.Tx, id uuid.UUID, now time.Time) (*ls.Request, error) {
	r, err := scanRequest(tx.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM service_requests r WHERE r.id = $1 FOR UPDATE`, id))
	if r, err = noRows(r, err, "lock service request"); err != nil || r == nil {
		return r, err
	}
	eff := r.Effective(now)
	if eff == r.Status {
		return r, nil
	}
	if r.Status == ls.StatusPendingConfirmation && r.SelectedOfferID != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE service_offers SET status = 'expired', updated_at = $2 WHERE id = $1 AND status = 'selected'`,
			*r.SelectedOfferID, now); err != nil {
			return nil, fmt.Errorf("expire selection: %w", err)
		}
	}
	var closedAt *time.Time
	note := "selection_timeout"
	if eff == ls.StatusExpired {
		closedAt, note = &now, "expired"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_requests SET status = $2, selected_offer_id = NULL, selection_expires_at = NULL,
		closed_at = $3, updated_at = $4 WHERE id = $1`, id, eff, closedAt, now); err != nil {
		return nil, fmt.Errorf("normalize service request: %w", err)
	}
	if err := insertRequestEvent(ctx, tx, id, eff, ls.RoleSystem, &note, now); err != nil {
		return nil, err
	}
	r.Status, r.SelectedOfferID, r.SelectionExpiresAt, r.ClosedAt, r.UpdatedAt = eff, nil, nil, closedAt, now
	return r, nil
}

func (repo *LocalServiceRepository) SelectOffer(ctx context.Context, p ports.SelectOfferParams) (bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	r, err := lockRequest(ctx, tx, p.RequestID, p.Now)
	if err != nil || r == nil {
		return false, err
	}
	if r.CustomerUserID != p.CustomerID || r.Status != ls.StatusOpen {
		return false, tx.Commit()
	}
	res, err := tx.ExecContext(ctx, `UPDATE service_offers SET status = 'selected', updated_at = $3
		WHERE id = $1 AND request_id = $2 AND status = 'pending'`, p.OfferID, p.RequestID, p.Now)
	if err != nil {
		return false, fmt.Errorf("select offer: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_requests SET status = 'pending_provider_confirmation', selected_offer_id = $2,
		selection_expires_at = $3, updated_at = $4 WHERE id = $1`, p.RequestID, p.OfferID, p.Deadline, p.Now); err != nil {
		return false, fmt.Errorf("mark request pending confirmation: %w", err)
	}
	if err := insertRequestEvent(ctx, tx, p.RequestID, ls.StatusPendingConfirmation, ls.RoleCustomer, nil, p.Now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (repo *LocalServiceRepository) RejectSelection(ctx context.Context, offerID, providerID uuid.UUID, now time.Time) (bool, error) {
	o, err := repo.GetOffer(ctx, offerID)
	if err != nil || o == nil {
		return false, err
	}
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	r, err := lockRequest(ctx, tx, o.RequestID, now)
	if err != nil || r == nil {
		return false, err
	}
	if r.Status != ls.StatusPendingConfirmation || r.SelectedOfferID == nil || *r.SelectedOfferID != offerID {
		return false, tx.Commit()
	}
	res, err := tx.ExecContext(ctx, `UPDATE service_offers SET status = 'rejected', updated_at = $3
		WHERE id = $1 AND provider_id = $2 AND status = 'selected'`, offerID, providerID, now)
	if err != nil {
		return false, fmt.Errorf("reject offer: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_requests SET status = 'open', selected_offer_id = NULL, selection_expires_at = NULL,
		updated_at = $2 WHERE id = $1`, o.RequestID, now); err != nil {
		return false, fmt.Errorf("reopen request: %w", err)
	}
	note := "provider_declined"
	if err := insertRequestEvent(ctx, tx, o.RequestID, ls.StatusOpen, ls.RoleProvider, &note, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// AcceptOffer is the qualified match. In one transaction: lock the request
// (and normalize it), re-check the selection is still this provider's and
// still within its deadline, return the existing match on a repeat, lock
// the provider row and check the balance, then insert the match, the
// MATCH_FEE ledger row (key per offer — can't apply twice) and the balance
// change, and move the offer/request to accepted/matched. The partial
// unique index on active matches is the last line against two matches.
func (repo *LocalServiceRepository) AcceptOffer(ctx context.Context, p ports.AcceptOfferParams) (*ports.AcceptResult, error) {
	o, err := repo.GetOffer(ctx, p.OfferID)
	if err != nil {
		return nil, err
	}
	if o == nil || o.ProviderID != p.ProviderID {
		return nil, apperr.NotFound("offer not found")
	}
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	r, err := lockRequest(ctx, tx, o.RequestID, p.Now)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("service request not found")
	}
	// Repeat accept (retry, double tap): the existing match, no new charge.
	var existing ls.Match
	err = tx.QueryRowContext(ctx, `SELECT `+matchColumns+` FROM service_matches m WHERE m.offer_id = $1`, p.OfferID).Scan(matchDest(&existing)...)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &ports.AcceptResult{Match: existing, AlreadyAccepted: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("find match: %w", err)
	}
	if r.Status != ls.StatusPendingConfirmation || r.SelectedOfferID == nil || *r.SelectedOfferID != p.OfferID {
		// Commit so a timed-out selection is recorded as such.
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, apperr.Conflict("this job is no longer waiting for your confirmation")
	}

	var (
		balance int
		status  ls.ProviderStatus
	)
	if err := tx.QueryRowContext(ctx, `SELECT credit_balance, status FROM service_providers WHERE id = $1 FOR UPDATE`, p.ProviderID).
		Scan(&balance, &status); err != nil {
		return nil, fmt.Errorf("lock provider: %w", err)
	}
	if status != ls.ProviderActive {
		return nil, apperr.Conflict("your provider profile is suspended")
	}
	if p.Charge && balance < p.Fee {
		return nil, apperr.InsufficientCredit(balance, p.Fee)
	}

	m := ls.Match{ID: p.MatchID, RequestID: r.ID, OfferID: p.OfferID, ProviderID: p.ProviderID, FeeCredits: p.Fee,
		FeeWaived: !p.Charge, Status: ls.MatchActive, CreatedAt: p.Now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO service_matches (id, request_id, offer_id, provider_id, fee_credits, fee_waived, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'active', $7)`, m.ID, m.RequestID, m.OfferID, m.ProviderID, m.FeeCredits, m.FeeWaived, m.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return nil, apperr.Conflict("this request already has a matched provider")
		}
		return nil, fmt.Errorf("insert match: %w", err)
	}
	if p.Charge && p.Fee > 0 {
		mid := m.ID
		if _, err := applyLedger(ctx, tx, ls.LedgerEntry{
			ProviderID: p.ProviderID, Type: ls.TxMatchFee, Amount: -p.Fee, Key: ls.MatchFeeKey(p.OfferID), MatchID: &mid, At: p.Now,
		}); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_offers SET status = 'accepted', updated_at = $2 WHERE id = $1`, p.OfferID, p.Now); err != nil {
		return nil, fmt.Errorf("accept offer: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_requests SET status = 'matched', selection_expires_at = NULL, selected_offer_id = NULL,
		updated_at = $2 WHERE id = $1`, r.ID, p.Now); err != nil {
		return nil, fmt.Errorf("mark request matched: %w", err)
	}
	if err := insertRequestEvent(ctx, tx, r.ID, ls.StatusMatched, ls.RoleProvider, nil, p.Now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit match: %w", err)
	}
	return &ports.AcceptResult{Match: m}, nil
}

func (repo *LocalServiceRepository) CurrentMatch(ctx context.Context, requestID uuid.UUID) (*ls.Match, error) {
	var m ls.Match
	// At most one match is active, and at most one completed (a completed
	// job closes the request); cancelled ones are history.
	err := repo.db.QueryRowContext(ctx, `SELECT `+matchColumns+` FROM service_matches m WHERE m.request_id = $1
		ORDER BY CASE m.status WHEN 'active' THEN 0 WHEN 'completed' THEN 1 ELSE 2 END, m.created_at DESC LIMIT 1`, requestID).Scan(matchDest(&m)...)
	return noRows(&m, err, "current match")
}

func (repo *LocalServiceRepository) MatchesByProvider(ctx context.Context, providerID uuid.UUID, limit int) ([]ports.MatchWithRequest, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT `+matchColumns+`, `+offerColumns+`, u.display_name, `+requestColumns+`
		FROM service_matches m
		JOIN service_offers o ON o.id = m.offer_id
		JOIN service_requests r ON r.id = m.request_id
		JOIN users u ON u.id = r.customer_user_id
		WHERE m.provider_id = $1
		ORDER BY (m.status = 'active') DESC, m.created_at DESC LIMIT $2`, providerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list provider matches: %w", err)
	}
	defer rows.Close()
	out := []ports.MatchWithRequest{}
	for rows.Next() {
		var item ports.MatchWithRequest
		before := append(matchDest(&item.Match), offerDest(&item.Offer)...)
		before = append(before, &item.CustomerName)
		r, err := scanRequest(multiScanner{rows: rows, before: before})
		if err != nil {
			return nil, fmt.Errorf("scan provider match: %w", err)
		}
		item.Request = *r
		out = append(out, item)
	}
	return out, rows.Err()
}

func (repo *LocalServiceRepository) CustomerName(ctx context.Context, userID uuid.UUID) (string, error) {
	var name string
	if err := repo.db.QueryRowContext(ctx, `SELECT display_name FROM users WHERE id = $1`, userID).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("customer name: %w", err)
	}
	return name, nil
}

// TransitionJob is a compare-and-set on the request status; the active match
// records when travel started and closes on completion.
func (repo *LocalServiceRepository) TransitionJob(ctx context.Context, t ports.JobTransition) (bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var closedAt *time.Time
	if t.To.IsClosed() {
		closedAt = &t.Now
	}
	res, err := tx.ExecContext(ctx, `UPDATE service_requests SET status = $3, closed_at = $4, updated_at = $5
		WHERE id = $1 AND status = $2`, t.RequestID, t.From, t.To, closedAt, t.Now)
	if err != nil {
		return false, fmt.Errorf("update job status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	switch t.To {
	case ls.StatusOnTheWay:
		_, err = tx.ExecContext(ctx, `UPDATE service_matches SET started_travel_at = $2 WHERE request_id = $1 AND status = 'active'`, t.RequestID, t.Now)
	case ls.StatusCompleted:
		_, err = tx.ExecContext(ctx, `UPDATE service_matches SET status = 'completed', closed_at = $2 WHERE request_id = $1 AND status = 'active'`, t.RequestID, t.Now)
	}
	if err != nil {
		return false, fmt.Errorf("update match: %w", err)
	}
	if err := insertRequestEvent(ctx, tx, t.RequestID, t.To, t.Actor, nil, t.Now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Cancel closes the request (customer) or reopens it (provider backs out),
// cancels the active match and pending selection, and applies a refund in
// the same transaction when the service decided one is due.
func (repo *LocalServiceRepository) Cancel(ctx context.Context, p ports.CancelParams) (bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	r, err := lockRequest(ctx, tx, p.RequestID, p.Now)
	if err != nil || r == nil {
		return false, err
	}
	if r.Status != p.From {
		return false, tx.Commit()
	}
	reason := string(p.Reason)
	if r.SelectedOfferID != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE service_offers SET status = 'expired', updated_at = $2 WHERE id = $1 AND status = 'selected'`,
			*r.SelectedOfferID, p.Now); err != nil {
			return false, fmt.Errorf("expire selection: %w", err)
		}
	}
	var cancelledOffer uuid.NullUUID
	err = tx.QueryRowContext(ctx, `UPDATE service_matches SET status = 'cancelled', cancelled_by = $2, cancel_reason = $3, closed_at = $4
		WHERE request_id = $1 AND status = 'active' RETURNING offer_id`, p.RequestID, p.Actor, reason, p.Now).Scan(&cancelledOffer)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("cancel match: %w", err)
	}
	if p.Actor == ls.RoleProvider && !cancelledOffer.Valid {
		return false, nil // provider may only back out of an active match
	}
	if p.Reopen {
		// The provider backed out: their offer can't be chosen again.
		if _, err := tx.ExecContext(ctx, `UPDATE service_offers SET status = 'rejected', updated_at = $2 WHERE id = $1`,
			cancelledOffer.UUID, p.Now); err != nil {
			return false, fmt.Errorf("reject backed-out offer: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE service_requests SET status = 'open', selected_offer_id = NULL, selection_expires_at = NULL,
			expires_at = $2, updated_at = $3 WHERE id = $1`, p.RequestID, p.ReopenExpiresAt, p.Now); err != nil {
			return false, fmt.Errorf("reopen request: %w", err)
		}
		if err := insertRequestEvent(ctx, tx, p.RequestID, ls.StatusOpen, p.Actor, &reason, p.Now); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE service_requests SET status = 'cancelled', selected_offer_id = NULL, selection_expires_at = NULL,
			cancel_reason = $2, closed_at = $3, updated_at = $3 WHERE id = $1`, p.RequestID, reason, p.Now); err != nil {
			return false, fmt.Errorf("cancel request: %w", err)
		}
		if err := insertRequestEvent(ctx, tx, p.RequestID, ls.StatusCancelled, p.Actor, &reason, p.Now); err != nil {
			return false, err
		}
	}
	if p.Refund != nil {
		if _, err := applyLedger(ctx, tx, *p.Refund); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (repo *LocalServiceRepository) CreateIssue(ctx context.Context, i *ls.Issue) error {
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO service_match_issues (id, match_id, reporter, reason, details, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, i.ID, i.MatchID, i.Reporter, i.Reason, i.Details, i.Status, i.CreatedAt); err != nil {
		return fmt.Errorf("insert match issue: %w", err)
	}
	return nil
}

func (repo *LocalServiceRepository) ListIssues(ctx context.Context, limit int) ([]ls.Issue, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT i.id, i.match_id, m.request_id, i.reporter, i.reason, i.details, i.status, i.created_at
		FROM service_match_issues i JOIN service_matches m ON m.id = i.match_id
		ORDER BY i.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list match issues: %w", err)
	}
	defer rows.Close()
	out := []ls.Issue{}
	for rows.Next() {
		var i ls.Issue
		if err := rows.Scan(&i.ID, &i.MatchID, &i.RequestID, &i.Reporter, &i.Reason, &i.Details, &i.Status, &i.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan match issue: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ---- wallet ----

// applyLedger appends one ledger row and moves the cached balance by the
// same amount, exactly once per key: the provider row is locked, and a key
// that already exists is a no-op (applied=false) so retries and duplicate
// webhooks change nothing. A result below zero violates the balance CHECK
// and aborts the transaction.
func applyLedger(ctx context.Context, tx *sql.Tx, e ls.LedgerEntry) (applied bool, err error) {
	var balance int
	if err := tx.QueryRowContext(ctx, `SELECT credit_balance FROM service_providers WHERE id = $1 FOR UPDATE`, e.ProviderID).Scan(&balance); err != nil {
		return false, fmt.Errorf("lock provider balance: %w", err)
	}
	next := balance + e.Amount
	if next < 0 {
		return false, apperr.InsufficientCredit(balance, -e.Amount)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO provider_credit_transactions (provider_id, type, amount, balance_after, idempotency_key, match_id, topup_id, note, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		e.ProviderID, e.Type, e.Amount, next, e.Key, e.MatchID, e.TopupID, e.Note, e.At)
	if err != nil {
		return false, fmt.Errorf("insert credit transaction: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_providers SET credit_balance = $2 WHERE id = $1`, e.ProviderID, next); err != nil {
		return false, fmt.Errorf("update provider balance: %w", err)
	}
	return true, nil
}

func (repo *LocalServiceRepository) Transactions(ctx context.Context, providerID uuid.UUID, limit int) ([]ls.Transaction, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT id, provider_id, type, amount, balance_after, match_id, topup_id, note, created_at
		FROM provider_credit_transactions WHERE provider_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, providerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list credit transactions: %w", err)
	}
	defer rows.Close()
	out := []ls.Transaction{}
	for rows.Next() {
		var t ls.Transaction
		if err := rows.Scan(&t.ID, &t.ProviderID, &t.Type, &t.Amount, &t.BalanceAfter, &t.MatchID, &t.TopupID, &t.Note, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan credit transaction: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (repo *LocalServiceRepository) CreateTopup(ctx context.Context, t *ls.Topup) error {
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO provider_topups (id, provider_id, package_id, amount, currency, credit_amount, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`, t.ID, t.ProviderID, t.PackageID, t.Amount, t.Currency, t.CreditAmount, t.Status, t.CreatedAt); err != nil {
		return fmt.Errorf("insert topup: %w", err)
	}
	return nil
}

func (repo *LocalServiceRepository) SetTopupSession(ctx context.Context, id uuid.UUID, sessionID string, now time.Time) error {
	// A webhook may already have recorded the session (and even paid it):
	// only fill it in when still empty.
	if _, err := repo.db.ExecContext(ctx, `UPDATE provider_topups SET stripe_checkout_session_id = $2, updated_at = $3
		WHERE id = $1 AND stripe_checkout_session_id IS NULL`, id, sessionID, now); err != nil {
		return fmt.Errorf("set topup session: %w", err)
	}
	return nil
}

func (repo *LocalServiceRepository) GetTopup(ctx context.Context, id uuid.UUID) (*ls.Topup, error) {
	t, err := scanTopup(repo.db.QueryRowContext(ctx, `SELECT `+topupColumns+` FROM provider_topups t WHERE t.id = $1`, id))
	return noRows(t, err, "get topup")
}

func (repo *LocalServiceRepository) GetTopupBySession(ctx context.Context, sessionID string) (*ls.Topup, error) {
	t, err := scanTopup(repo.db.QueryRowContext(ctx, `SELECT `+topupColumns+` FROM provider_topups t WHERE t.stripe_checkout_session_id = $1`, sessionID))
	return noRows(t, err, "get topup by session")
}

func (repo *LocalServiceRepository) RecentTopups(ctx context.Context, providerID uuid.UUID, limit int) ([]ls.Topup, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT `+topupColumns+` FROM provider_topups t WHERE t.provider_id = $1
		ORDER BY t.created_at DESC LIMIT $2`, providerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list topups: %w", err)
	}
	defer rows.Close()
	out := []ls.Topup{}
	for rows.Next() {
		t, err := scanTopup(rows)
		if err != nil {
			return nil, fmt.Errorf("scan topup: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// MarkTopupPaid locks the top-up, re-checks it against the verified payment
// (session, amount, currency) and, unless it is already paid or refunded,
// marks it paid and appends the TOP_UP ledger row (key per top-up) in the
// same transaction. A late "paid" after "expired"/"failed" still credits:
// the money was taken.
func (repo *LocalServiceRepository) MarkTopupPaid(ctx context.Context, p ports.TopupPaid) (bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	t, err := scanTopup(tx.QueryRowContext(ctx, `SELECT `+topupColumns+` FROM provider_topups t WHERE t.id = $1 FOR UPDATE`, p.TopupID))
	if t, err = noRows(t, err, "lock topup"); err != nil || t == nil {
		return false, err
	}
	if t.Status == ls.TopupPaid || t.Status == ls.TopupRefunded {
		return false, nil
	}
	if (t.StripeCheckoutSessionID != nil && *t.StripeCheckoutSessionID != p.SessionID) || t.Amount != p.Amount || t.Currency != p.Currency {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_topups SET status = 'paid', stripe_checkout_session_id = $2,
		stripe_payment_intent_id = COALESCE($3, stripe_payment_intent_id), paid_at = $4, updated_at = $4 WHERE id = $1`,
		t.ID, p.SessionID, p.PaymentIntentID, p.Now); err != nil {
		return false, fmt.Errorf("mark topup paid: %w", err)
	}
	tid := t.ID
	applied, err := applyLedger(ctx, tx, ls.LedgerEntry{
		ProviderID: t.ProviderID, Type: ls.TxTopUp, Amount: t.CreditAmount, Key: ls.TopUpKey(t.ID), TopupID: &tid, At: p.Now,
	})
	if err != nil {
		return false, err
	}
	return applied, tx.Commit()
}

func (repo *LocalServiceRepository) MarkTopup(ctx context.Context, id uuid.UUID, to ls.TopupStatus, now time.Time) error {
	if _, err := repo.db.ExecContext(ctx, `UPDATE provider_topups SET status = $2, updated_at = $3 WHERE id = $1 AND status = 'pending'`,
		id, to, now); err != nil {
		return fmt.Errorf("mark topup %s: %w", to, err)
	}
	return nil
}

func (repo *LocalServiceRepository) MarkTopupRefundedByPaymentIntent(ctx context.Context, paymentIntentID string, now time.Time) (*ls.Topup, error) {
	t, err := scanTopup(repo.db.QueryRowContext(ctx, `UPDATE provider_topups t SET status = 'refunded', updated_at = $2
		WHERE t.stripe_payment_intent_id = $1 AND t.status = 'paid' RETURNING `+topupColumns, paymentIntentID, now))
	return noRows(t, err, "mark topup refunded")
}

func (repo *LocalServiceRepository) AdjustCredit(ctx context.Context, e ls.LedgerEntry) (*ls.Transaction, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := applyLedger(ctx, tx, e); err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Code == apperr.CodeInsufficientCredit {
			return nil, apperr.Conflict("the adjustment would make the balance negative")
		}
		return nil, err
	}
	var t ls.Transaction
	if err := tx.QueryRowContext(ctx, `SELECT id, provider_id, type, amount, balance_after, match_id, topup_id, note, created_at
		FROM provider_credit_transactions WHERE idempotency_key = $1`, e.Key).
		Scan(&t.ID, &t.ProviderID, &t.Type, &t.Amount, &t.BalanceAfter, &t.MatchID, &t.TopupID, &t.Note, &t.CreatedAt); err != nil {
		return nil, fmt.Errorf("read adjustment: %w", err)
	}
	return &t, tx.Commit()
}

// ---- operator ----

func (repo *LocalServiceRepository) ListAllProviders(ctx context.Context, limit int) ([]ls.Provider, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT `+providerColumns+` FROM service_providers p ORDER BY p.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list all providers: %w", err)
	}
	defer rows.Close()
	out := []ls.Provider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (repo *LocalServiceRepository) SetProviderVerified(ctx context.Context, id uuid.UUID, verifiedAt *time.Time, now time.Time) error {
	if _, err := repo.db.ExecContext(ctx, `UPDATE service_providers SET verified_at = $2, updated_at = $3 WHERE id = $1`, id, verifiedAt, now); err != nil {
		return fmt.Errorf("set provider verified: %w", err)
	}
	return nil
}

func (repo *LocalServiceRepository) SetProviderStatus(ctx context.Context, id uuid.UUID, status ls.ProviderStatus, now time.Time) error {
	if _, err := repo.db.ExecContext(ctx, `UPDATE service_providers SET status = $2, updated_at = $3 WHERE id = $1`, id, status, now); err != nil {
		return fmt.Errorf("set provider status: %w", err)
	}
	return nil
}
