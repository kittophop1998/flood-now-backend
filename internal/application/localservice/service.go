// Package localservice contains the local-services use cases: provider
// profiles, customer service requests, offers, the qualified match (and its
// fee), job progress, cancellation/refunds and the provider credit wallet.
//
// Every read and write is authorized here from the signed-in user: the
// customer sees their own requests; a provider sees requests only redacted
// (approximate area, no contact) until they are the matched party. Nothing
// here touches SOS or volunteer helpers.
package localservice

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	ls "floodnow-api/internal/domain/localservice"
	"floodnow-api/internal/ports"
)

const (
	defaultBrowseRadiusM = 20_000
	maxBrowseRadiusM     = 50_000
	defaultBrowseLimit   = 50
	maxBrowseLimit       = 100
	nearbyRequestLimit   = 50
	listLimit            = 30
	transactionsLimit    = 50
	topupsLimit          = 10
	adminListLimit       = 200
)

// Config wires the service. Payments nil means PromptPay top-ups are off.
type Config struct {
	Policy   ls.BillingPolicy
	Packages []ls.Package
	Payments ports.PaymentProvider
}

type Service struct {
	repo  ports.LocalServiceRepository
	clock ports.Clock
	cfg   Config
}

func NewService(repo ports.LocalServiceRepository, clock ports.Clock, cfg Config) *Service {
	return &Service{repo: repo, clock: clock, cfg: cfg}
}

func (s *Service) Policy() ls.BillingPolicy { return s.cfg.Policy }
func (s *Service) Packages() []ls.Package   { return s.cfg.Packages }
func (s *Service) TopupsEnabled() bool      { return s.cfg.Payments != nil }

// Now is the service clock, so presenters derive statuses at the same instant.
func (s *Service) Now() time.Time { return s.clock.Now() }

// ---- Provider profile (owner) ----

// MyProvider returns the user's provider profile, or nil if they have none.
func (s *Service) MyProvider(ctx context.Context, userID uuid.UUID) (*ls.Provider, error) {
	return s.repo.GetProviderByOwner(ctx, userID)
}

func (s *Service) requireProvider(ctx context.Context, userID uuid.UUID) (*ls.Provider, error) {
	p, err := s.repo.GetProviderByOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperr.NotFound("create a service provider profile first")
	}
	return p, nil
}

// SaveProvider creates the user's profile (granting the welcome credit
// once, when credit is on) or replaces its editable fields. Balance,
// verification and suspension are never set from here.
func (s *Service) SaveProvider(ctx context.Context, userID uuid.UUID, in ls.ProviderInput) (*ls.Provider, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	existing, err := s.repo.GetProviderByOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		in.Apply(existing)
		existing.UpdatedAt = now
		if err := s.repo.UpdateProvider(ctx, existing); err != nil {
			return nil, err
		}
		return s.repo.GetProvider(ctx, existing.ID)
	}
	p := &ls.Provider{ID: uuid.New(), OwnerUserID: userID, Status: ls.ProviderActive, CreatedAt: now, UpdatedAt: now}
	in.Apply(p)
	var welcome *ls.LedgerEntry
	if pol := s.cfg.Policy; pol.CreditEnabled && pol.WelcomeCredit > 0 {
		welcome = &ls.LedgerEntry{ProviderID: p.ID, Type: ls.TxWelcomeCredit, Amount: pol.WelcomeCredit, Key: ls.WelcomeKey(p.ID), At: now}
	}
	if err := s.repo.CreateProvider(ctx, p, welcome); err != nil {
		return nil, err
	}
	log.Printf("service provider %s created", p.ID)
	return s.repo.GetProvider(ctx, p.ID)
}

// SetAvailability is the "พร้อมรับงาน" toggle.
func (s *Service) SetAvailability(ctx context.Context, userID uuid.UUID, available bool) (*ls.Provider, error) {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.SetProviderAvailability(ctx, p.ID, available, s.clock.Now()); err != nil {
		return nil, err
	}
	return s.repo.GetProvider(ctx, p.ID)
}

// Summary is the provider dashboard's counts.
type Summary struct {
	NearbyOpen    int
	OffersPending int
	ActiveJobs    int
	CompletedJobs int
}

func (s *Service) Summary(ctx context.Context, p *ls.Provider) (Summary, error) {
	var sum Summary
	if p.CanWork() {
		reqs, err := s.openNear(ctx, p)
		if err != nil {
			return sum, err
		}
		sum.NearbyOpen = len(reqs)
	}
	offers, err := s.MyOffersFor(ctx, p)
	if err != nil {
		return sum, err
	}
	for _, o := range offers {
		if o.Effective == ls.OfferPending || o.Effective == ls.OfferSelected {
			sum.OffersPending++
		}
	}
	jobs, err := s.repo.MatchesByProvider(ctx, p.ID, listLimit)
	if err != nil {
		return sum, err
	}
	for _, j := range jobs {
		switch j.Status {
		case ls.MatchActive:
			sum.ActiveJobs++
		case ls.MatchCompleted:
			sum.CompletedJobs++
		}
	}
	return sum, nil
}

// ---- Public browse ----

// BrowseProviders lists active providers around a point (public; no
// contact details). Ordering is fixed: available first, then nearest —
// nothing paid or sponsored changes it.
func (s *Service) BrowseProviders(ctx context.Context, q ports.ProviderQuery) ([]ports.ProviderWithDistance, error) {
	if q.Latitude < -90 || q.Latitude > 90 || q.Longitude < -180 || q.Longitude > 180 {
		return nil, apperr.Validation("location is invalid", map[string]string{"lat": "must be a valid latitude/longitude"})
	}
	if q.Category != nil && !q.Category.Valid() {
		return nil, apperr.Validation("category is invalid", map[string]string{"category": "must be a known service category"})
	}
	if q.RadiusM <= 0 {
		q.RadiusM = defaultBrowseRadiusM
	}
	if q.RadiusM > maxBrowseRadiusM {
		q.RadiusM = maxBrowseRadiusM
	}
	if q.Limit <= 0 {
		q.Limit = defaultBrowseLimit
	}
	if q.Limit > maxBrowseLimit {
		q.Limit = maxBrowseLimit
	}
	q.Text = strings.TrimSpace(q.Text)
	if len([]rune(q.Text)) > 60 {
		q.Text = string([]rune(q.Text)[:60])
	}
	return s.repo.ListProviders(ctx, q)
}

// PublicProvider returns one active provider (public view). at, when set,
// adds the distance from there.
func (s *Service) PublicProvider(ctx context.Context, id uuid.UUID, at *[2]float64) (*ports.ProviderWithDistance, error) {
	p, err := s.repo.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil || p.Status != ls.ProviderActive {
		return nil, apperr.NotFound("service provider not found")
	}
	out := &ports.ProviderWithDistance{Provider: *p, DistanceM: -1}
	if at != nil {
		out.DistanceM = ls.DistanceM(at[0], at[1], p.Latitude, p.Longitude)
	}
	return out, nil
}

// ---- Views ----

// OfferView is an offer with its status as of now.
type OfferView struct {
	ports.OfferWithProvider
	Effective ls.OfferStatus
}

// RequestView is a request as one of its parties sees it.
type RequestView struct {
	ls.Request
	Effective ls.Status
	Role      ls.Role
	Events    []ls.Event
	// Offers: customer only.
	Offers []OfferView
	// Match is the current match (active or completed); nil otherwise.
	Match *ls.Match
	// MatchedOffer is the agreed offer (price/estimate, ETA).
	MatchedOffer *ls.Offer
	// Provider is the matched provider, with contact (customer only).
	Provider *ls.Provider
	// CustomerName (provider view only).
	CustomerName string
	// RefundUntil: while the customer could still cancel for a refund.
	RefundUntil *time.Time
}

// roleOf decides the caller's relation to a request. The matched provider
// is a party only while their match is active or completed.
func (s *Service) roleOf(ctx context.Context, userID uuid.UUID, r *ls.Request) (ls.Role, *ls.Match, *ls.Provider, error) {
	m, err := s.repo.CurrentMatch(ctx, r.ID)
	if err != nil {
		return "", nil, nil, err
	}
	if m != nil && m.Status == ls.MatchCancelled {
		m = nil
	}
	var mp *ls.Provider
	if m != nil {
		if mp, err = s.repo.GetProvider(ctx, m.ProviderID); err != nil {
			return "", nil, nil, err
		}
	}
	switch {
	case r.CustomerUserID == userID:
		return ls.RoleCustomer, m, mp, nil
	case mp != nil && mp.OwnerUserID == userID:
		return ls.RoleProvider, m, mp, nil
	}
	return "", nil, nil, nil
}

func (s *Service) view(ctx context.Context, r *ls.Request, role ls.Role, m *ls.Match, mp *ls.Provider) (*RequestView, error) {
	now := s.clock.Now()
	v := &RequestView{Request: *r, Effective: r.Effective(now), Role: role, Match: m}
	events, err := s.repo.RequestEvents(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	v.Events = events
	if m != nil {
		o, err := s.repo.GetOffer(ctx, m.OfferID)
		if err != nil {
			return nil, err
		}
		v.MatchedOffer = o
		if role == ls.RoleCustomer {
			v.Provider = mp
			if m.Charged() && m.Status == ls.MatchActive && v.Effective == ls.StatusMatched && m.StartedTravelAt == nil {
				until := m.CreatedAt.Add(s.cfg.Policy.RefundGrace)
				if now.Before(until) {
					v.RefundUntil = &until
				}
			}
		} else {
			name, err := s.repo.CustomerName(ctx, r.CustomerUserID)
			if err != nil {
				return nil, err
			}
			v.CustomerName = name
		}
	}
	if role == ls.RoleCustomer {
		offers, err := s.repo.OffersForRequest(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for _, o := range offers {
			v.Offers = append(v.Offers, OfferView{OfferWithProvider: o, Effective: ls.EffectiveOffer(o.Offer, *r, now)})
		}
	}
	return v, nil
}

// ---- Customer ----

// CreateRequest files a new service request (session required — the
// handler passes the signed-in user). Separate from SOS: it is commercial,
// may be quoted and is never shown to volunteer helpers.
func (s *Service) CreateRequest(ctx context.Context, in ls.NewRequestInput) (*RequestView, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if in.ClientID != nil {
		existing, err := s.repo.FindRequestByClientID(ctx, in.CustomerUserID, *in.ClientID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return s.view(ctx, existing, ls.RoleCustomer, nil, nil)
		}
	}
	now := s.clock.Now()
	n, err := s.repo.CountOpenRequests(ctx, in.CustomerUserID, now)
	if err != nil {
		return nil, err
	}
	if n >= ls.MaxOpenRequestsPerCustomer {
		return nil, apperr.Conflict("you already have several open service requests; cancel one first")
	}
	vehicle := ls.Trimmed(in.VehicleInfo)
	if !in.Category.TakesVehicleInfo() {
		vehicle = nil
	}
	r := &ls.Request{
		ID: uuid.New(), CustomerUserID: in.CustomerUserID, ClientID: in.ClientID, Category: in.Category,
		Description: ls.Trimmed(in.Description), VehicleInfo: vehicle, ImageKey: ls.Trimmed(in.ImageKey),
		Latitude: in.Latitude, Longitude: in.Longitude, LocationName: ls.Trimmed(in.LocationName),
		ContactPhone: strings.TrimSpace(in.ContactPhone), Status: ls.StatusOpen,
		ExpiresAt: now.Add(s.cfg.Policy.RequestTTL), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.CreateRequest(ctx, r); err != nil {
		return nil, err
	}
	log.Printf("service request %s created category=%s", r.ID, r.Category)
	return s.view(ctx, r, ls.RoleCustomer, nil, nil)
}

// MyRequests lists the customer's own requests, newest first.
func (s *Service) MyRequests(ctx context.Context, userID uuid.UUID) ([]RequestView, error) {
	rs, err := s.repo.ListRequestsByCustomer(ctx, userID, listLimit)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	out := make([]RequestView, 0, len(rs))
	for i := range rs {
		// Full views (offers, match) only for requests still in progress;
		// the list stays small so this is bounded.
		if rs[i].Effective(now).IsClosed() {
			out = append(out, RequestView{Request: rs[i], Effective: rs[i].Effective(now), Role: ls.RoleCustomer})
			continue
		}
		v, err := s.GetRequest(ctx, userID, rs[i].ID)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// GetRequest returns a request to its customer or matched provider; anyone
// else gets NOT_FOUND so ids reveal nothing.
func (s *Service) GetRequest(ctx context.Context, userID, id uuid.UUID) (*RequestView, error) {
	r, err := s.repo.GetRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("service request not found")
	}
	role, m, mp, err := s.roleOf(ctx, userID, r)
	if err != nil {
		return nil, err
	}
	if role == "" {
		return nil, apperr.NotFound("service request not found")
	}
	return s.view(ctx, r, role, m, mp)
}

func (s *Service) customerRequest(ctx context.Context, userID, id uuid.UUID) (*ls.Request, error) {
	r, err := s.repo.GetRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil || r.CustomerUserID != userID {
		return nil, apperr.NotFound("service request not found")
	}
	return r, nil
}

// SelectOffer is the customer's "เลือกร้านนี้". It only asks the provider to
// confirm: no credit moves until the provider accepts.
func (s *Service) SelectOffer(ctx context.Context, userID, requestID, offerID uuid.UUID) (*RequestView, error) {
	r, err := s.customerRequest(ctx, userID, requestID)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	if eff := r.Effective(now); eff != ls.StatusOpen {
		return nil, apperr.Conflict("you can only choose a provider while the request is open (current status: " + string(eff) + ")")
	}
	o, err := s.repo.GetOffer(ctx, offerID)
	if err != nil {
		return nil, err
	}
	if o == nil || o.RequestID != r.ID {
		return nil, apperr.NotFound("offer not found")
	}
	if ls.EffectiveOffer(*o, *r, now) != ls.OfferPending {
		return nil, apperr.Conflict("this offer can no longer be chosen")
	}
	p, err := s.repo.GetProvider(ctx, o.ProviderID)
	if err != nil {
		return nil, err
	}
	if p == nil || p.Status != ls.ProviderActive {
		return nil, apperr.Conflict("this provider isn't taking jobs right now")
	}
	ok, err := s.repo.SelectOffer(ctx, ports.SelectOfferParams{
		RequestID: r.ID, OfferID: o.ID, CustomerID: userID, Now: now, Deadline: now.Add(s.cfg.Policy.ConfirmTimeout),
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Conflict("the request changed in the meantime; reload and try again")
	}
	log.Printf("service request %s: offer %s selected", r.ID, o.ID)
	return s.GetRequest(ctx, userID, r.ID)
}

// UpdateStatus applies a post-match job step (provider: on the way →
// arrived → completed; the customer may also confirm completion).
func (s *Service) UpdateStatus(ctx context.Context, userID, id uuid.UUID, to ls.Status) (*RequestView, error) {
	if !to.Valid() {
		return nil, apperr.Validation("status is invalid", map[string]string{"status": "must be one of on_the_way, arrived, completed"})
	}
	r, err := s.repo.GetRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("service request not found")
	}
	role, _, _, err := s.roleOf(ctx, userID, r)
	if err != nil {
		return nil, err
	}
	if role == "" {
		return nil, apperr.NotFound("service request not found")
	}
	now := s.clock.Now()
	from := r.Effective(now)
	if !ls.CanTransition(from, to, role) {
		return nil, apperr.Conflict("this status change isn't allowed now (current status: " + string(from) + ")")
	}
	ok, err := s.repo.TransitionJob(ctx, ports.JobTransition{RequestID: id, From: from, To: to, Actor: role, Now: now})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Conflict("the request changed in the meantime; reload and try again")
	}
	log.Printf("service request %s %s -> %s by %s", id, from, to, role)
	return s.GetRequest(ctx, userID, id)
}

// Cancel ends a request (customer) or backs a provider out of a job. A
// customer cancelling within the refund grace, before the provider set
// off, refunds the provider's match fee as a new REFUND ledger row. A
// provider backing out gets no refund and the request reopens so the
// customer can choose someone else.
func (s *Service) Cancel(ctx context.Context, userID, id uuid.UUID, reason ls.CancelReason, note *string) (*RequestView, error) {
	if !reason.Valid() {
		return nil, apperr.Validation("reason is invalid", map[string]string{"reason": "must be a known cancel reason"})
	}
	if note != nil && len([]rune(*note)) > 500 {
		return nil, apperr.Validation("note is too long", map[string]string{"note": "must be 500 characters or fewer"})
	}
	r, err := s.repo.GetRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("service request not found")
	}
	role, m, _, err := s.roleOf(ctx, userID, r)
	if err != nil {
		return nil, err
	}
	if role == "" {
		return nil, apperr.NotFound("service request not found")
	}
	now := s.clock.Now()
	from := r.Effective(now)
	if !ls.CanCancel(from, role) {
		return nil, apperr.Conflict("this request can't be cancelled now (current status: " + string(from) + ")")
	}
	p := ports.CancelParams{RequestID: id, From: from, Actor: role, Reason: reason, Note: ls.Trimmed(note), Now: now}
	if role == ls.RoleProvider {
		p.Reopen = true
		p.ReopenExpiresAt = r.ExpiresAt
		if min := now.Add(s.cfg.Policy.RequestTTL); p.ReopenExpiresAt.Before(min) {
			p.ReopenExpiresAt = min
		}
	}
	if m != nil && ls.RefundEligible(*m, from, role, now, s.cfg.Policy.RefundGrace) {
		mid := m.ID
		p.Refund = &ls.LedgerEntry{ProviderID: m.ProviderID, Type: ls.TxRefund, Amount: m.FeeCredits, Key: ls.RefundKey(m.ID), MatchID: &mid, At: now}
	}
	ok, err := s.repo.Cancel(ctx, p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Conflict("the request changed in the meantime; reload and try again")
	}
	log.Printf("service request %s cancelled by %s from %s (reason=%s refund=%v)", id, role, from, reason, p.Refund != nil)
	if role == ls.RoleProvider {
		// The provider is no longer a party to it.
		return nil, nil
	}
	return s.GetRequest(ctx, userID, id)
}

// ReportIssue records a "couldn't reach them / no-show" style problem on
// the current match, from either party. No automated action follows.
func (s *Service) ReportIssue(ctx context.Context, userID, id uuid.UUID, reason ls.IssueReason, details *string) error {
	if !reason.Valid() {
		return apperr.Validation("reason is invalid", map[string]string{"reason": "must be a known issue reason"})
	}
	if details != nil && len([]rune(*details)) > 1000 {
		return apperr.Validation("details are too long", map[string]string{"details": "must be 1000 characters or fewer"})
	}
	r, err := s.repo.GetRequest(ctx, id)
	if err != nil {
		return err
	}
	if r == nil {
		return apperr.NotFound("service request not found")
	}
	role, m, _, err := s.roleOf(ctx, userID, r)
	if err != nil {
		return err
	}
	if role == "" {
		return apperr.NotFound("service request not found")
	}
	if m == nil {
		return apperr.Conflict("there is no matched provider to report")
	}
	issue := &ls.Issue{ID: uuid.New(), MatchID: m.ID, RequestID: r.ID, Reporter: role, Reason: reason, Details: ls.Trimmed(details), Status: "open", CreatedAt: s.clock.Now()}
	if err := s.repo.CreateIssue(ctx, issue); err != nil {
		return err
	}
	log.Printf("service match %s issue reported by %s: %s", m.ID, role, reason)
	return nil
}

// ---- Provider ----

func (s *Service) openNear(ctx context.Context, p *ls.Provider) ([]ports.RequestWithDistance, error) {
	return s.repo.OpenRequestsNear(ctx, ports.OpenRequestQuery{
		ProviderID: p.ID, ExcludeCustomerID: p.OwnerUserID, Latitude: p.Latitude, Longitude: p.Longitude,
		RadiusM: float64(p.ServiceRadiusM), Categories: p.Categories, Now: s.clock.Now(), Limit: nearbyRequestLimit,
	})
}

// NearbyRequests lists requests the provider can quote on: their
// categories, inside their service radius, still taking offers. Redacted
// (the handler shows only an approximate point; no contact). Viewing is
// free, whatever the balance.
func (s *Service) NearbyRequests(ctx context.Context, userID uuid.UUID) ([]ports.RequestWithDistance, error) {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !p.CanWork() {
		return nil, apperr.Conflict("turn on “available for jobs” to see requests near you")
	}
	return s.openNear(ctx, p)
}

// Dismiss hides a request from the provider's list.
func (s *Service) Dismiss(ctx context.Context, userID, requestID uuid.UUID) error {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return err
	}
	r, err := s.repo.GetRequest(ctx, requestID)
	if err != nil {
		return err
	}
	if r == nil {
		return apperr.NotFound("service request not found")
	}
	return s.repo.DismissRequest(ctx, requestID, p.ID, s.clock.Now())
}

// SendOffer creates or edits the provider's (still pending) offer. Free.
func (s *Service) SendOffer(ctx context.Context, userID, requestID uuid.UUID, in ls.OfferInput) (*ls.Offer, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !p.CanWork() {
		return nil, apperr.Conflict("turn on “available for jobs” to send offers")
	}
	r, err := s.repo.GetRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if r == nil || r.CustomerUserID == userID || !p.HasCategory(r.Category) ||
		ls.DistanceM(p.Latitude, p.Longitude, r.Latitude, r.Longitude) > float64(p.ServiceRadiusM) {
		return nil, apperr.NotFound("service request not found")
	}
	now := s.clock.Now()
	if !r.Effective(now).TakesOffers() {
		return nil, apperr.Conflict("this request no longer takes offers")
	}
	o := &ls.Offer{
		ID: uuid.New(), RequestID: r.ID, ProviderID: p.ID, PriceTHB: in.PriceTHB, ETAMinutes: in.ETAMinutes,
		Note: ls.Trimmed(in.Note), Status: ls.OfferPending, CreatedAt: now, UpdatedAt: now,
	}
	ok, err := s.repo.UpsertOffer(ctx, o, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Conflict("this offer can't be changed any more")
	}
	return o, nil
}

// ProviderOfferView is an offer with its status as of now.
type ProviderOfferView struct {
	ports.OfferWithRequest
	Effective ls.OfferStatus
}

// MyOffers lists the provider's offers (newest first).
func (s *Service) MyOffers(ctx context.Context, userID uuid.UUID) ([]ProviderOfferView, error) {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.MyOffersFor(ctx, p)
}

func (s *Service) MyOffersFor(ctx context.Context, p *ls.Provider) ([]ProviderOfferView, error) {
	offers, err := s.repo.OffersByProvider(ctx, p.ID, listLimit)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	out := make([]ProviderOfferView, 0, len(offers))
	for _, o := range offers {
		out = append(out, ProviderOfferView{OfferWithRequest: o, Effective: ls.EffectiveOffer(o.Offer, o.Request, now)})
	}
	return out, nil
}

// AcceptOffer is the provider confirming the customer's selection: the
// qualified match. The match fee is deducted here, exactly once, in the
// transaction that creates the match — never on viewing, offering or the
// customer's selection. Not enough credit refuses the match
// (INSUFFICIENT_CREDIT) and changes nothing; after topping up the
// provider must accept again (nothing matches automatically).
func (s *Service) AcceptOffer(ctx context.Context, userID, offerID uuid.UUID) (*RequestView, error) {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, err
	}
	if p.Status != ls.ProviderActive {
		return nil, apperr.Conflict("your provider profile is suspended")
	}
	o, err := s.repo.GetOffer(ctx, offerID)
	if err != nil {
		return nil, err
	}
	if o == nil || o.ProviderID != p.ID {
		return nil, apperr.NotFound("offer not found")
	}
	// Whether the selection is still this provider's (and in time) is
	// decided under the request lock in the repository — a check here would
	// race a concurrent accept and turn an idempotent repeat into an error.
	now := s.clock.Now()
	res, err := s.repo.AcceptOffer(ctx, ports.AcceptOfferParams{
		OfferID: o.ID, ProviderID: p.ID, MatchID: uuid.New(),
		Fee: s.cfg.Policy.MatchFee, Charge: s.cfg.Policy.ChargesMatch(), Now: now,
	})
	if err != nil {
		return nil, err
	}
	if !res.AlreadyAccepted {
		log.Printf("service match %s created request=%s provider=%s fee=%d waived=%v", res.Match.ID, o.RequestID, p.ID, res.Match.FeeCredits, res.Match.FeeWaived)
	}
	return s.GetRequest(ctx, userID, o.RequestID)
}

// RejectOffer declines the customer's selection (no charge); the request
// reopens so the customer can pick someone else.
func (s *Service) RejectOffer(ctx context.Context, userID, offerID uuid.UUID) error {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return err
	}
	o, err := s.repo.GetOffer(ctx, offerID)
	if err != nil {
		return err
	}
	if o == nil || o.ProviderID != p.ID {
		return apperr.NotFound("offer not found")
	}
	ok, err := s.repo.RejectSelection(ctx, o.ID, p.ID, s.clock.Now())
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Conflict("this job is no longer waiting for your confirmation")
	}
	log.Printf("service offer %s rejected by provider", o.ID)
	return nil
}

// JobView is a provider's job with the customer's contact (matched only).
type JobView struct {
	ports.MatchWithRequest
	Effective ls.Status
}

// MyJobs lists the provider's matches, newest first.
func (s *Service) MyJobs(ctx context.Context, userID uuid.UUID) ([]JobView, error) {
	p, err := s.requireProvider(ctx, userID)
	if err != nil {
		return nil, err
	}
	jobs, err := s.repo.MatchesByProvider(ctx, p.ID, listLimit)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	out := make([]JobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, JobView{MatchWithRequest: j, Effective: j.Request.Effective(now)})
	}
	return out, nil
}
