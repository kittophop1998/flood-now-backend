package localservice

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

// Status is a service request's lifecycle:
//
//	open → pending_provider_confirmation → matched → on_the_way → arrived → completed
//
// with cancelled/expired as terminal states. A selection the provider
// rejects (or doesn't confirm in time) returns the request to open; a
// provider cancelling an accepted job reopens it too.
type Status string

const (
	StatusOpen                Status = "open"
	StatusPendingConfirmation Status = "pending_provider_confirmation"
	StatusMatched             Status = "matched"
	StatusOnTheWay            Status = "on_the_way"
	StatusArrived             Status = "arrived"
	StatusCompleted           Status = "completed"
	StatusCancelled           Status = "cancelled"
	StatusExpired             Status = "expired"
)

func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusPendingConfirmation, StatusMatched, StatusOnTheWay, StatusArrived, StatusCompleted, StatusCancelled, StatusExpired:
		return true
	}
	return false
}

// IsClosed reports a terminal status.
func (s Status) IsClosed() bool {
	return s == StatusCompleted || s == StatusCancelled || s == StatusExpired
}

// IsMatched reports a status with an active match (contact unlocked).
func (s Status) IsMatched() bool {
	return s == StatusMatched || s == StatusOnTheWay || s == StatusArrived
}

// TakesOffers reports whether providers may still send/edit offers.
func (s Status) TakesOffers() bool { return s == StatusOpen || s == StatusPendingConfirmation }

// Role is how a user relates to a request.
type Role string

const (
	RoleCustomer Role = "customer"
	RoleProvider Role = "provider"
	RoleSystem   Role = "system"
)

// jobTransitions are the post-match status moves: from → to → who.
// Matching itself (pending → matched) is the provider's accept, and
// cancelling has its own rules (CanCancel); neither is listed here.
var jobTransitions = map[Status]map[Status][]Role{
	StatusMatched:  {StatusOnTheWay: {RoleProvider}},
	StatusOnTheWay: {StatusArrived: {RoleProvider}},
	StatusArrived:  {StatusCompleted: {RoleProvider, RoleCustomer}},
}

// CanTransition reports whether role may move a matched job from → to.
func CanTransition(from, to Status, role Role) bool {
	return slices.Contains(jobTransitions[from][to], role)
}

// CanCancel reports whether role may cancel a request in status s. The
// customer may cancel any time before it's done; the matched provider may
// back out before arriving (the request then reopens for other offers).
func CanCancel(s Status, role Role) bool {
	switch role {
	case RoleCustomer:
		return !s.IsClosed()
	case RoleProvider:
		return s == StatusMatched || s == StatusOnTheWay
	}
	return false
}

// Request is a customer's service request.
type Request struct {
	ID             uuid.UUID
	CustomerUserID uuid.UUID
	ClientID       *string
	Category       Category
	Description    *string
	VehicleInfo    *string
	ImageKey       *string
	// Latitude/Longitude are exact and private: only the customer and the
	// matched provider see them. Everyone else gets ApproximateCoordinate.
	Latitude           float64
	Longitude          float64
	LocationName       *string
	ContactPhone       string
	Status             Status
	SelectedOfferID    *uuid.UUID
	SelectionExpiresAt *time.Time
	ExpiresAt          time.Time
	CancelReason       *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ClosedAt           *time.Time
}

// SelectionTimedOut reports a pending selection the provider didn't confirm
// in time (the request is effectively open again).
func (r Request) SelectionTimedOut(now time.Time) bool {
	return r.Status == StatusPendingConfirmation && r.SelectionExpiresAt != nil && !now.Before(*r.SelectionExpiresAt)
}

// Effective is the status as of now: a stored open/pending request past its
// expiry is expired, and a pending selection past its confirmation deadline
// is open again. Stored rows catch up lazily (the next write normalizes
// them), so every read goes through this.
func (r Request) Effective(now time.Time) Status {
	if r.Status.TakesOffers() && !now.Before(r.ExpiresAt) {
		return StatusExpired
	}
	if r.SelectionTimedOut(now) {
		return StatusOpen
	}
	return r.Status
}

// CancelReason says why a request/match was cancelled; the "couldn't
// reach them" ones double as the simple no-response record.
type CancelReason string

const (
	CancelChangedMind         CancelReason = "changed_mind"
	CancelFoundOther          CancelReason = "found_other"
	CancelNoResponse          CancelReason = "no_response"
	CancelUnableToContact     CancelReason = "unable_to_contact"
	CancelProviderUnavailable CancelReason = "provider_unavailable"
	CancelCustomerUnreachable CancelReason = "customer_unreachable"
	CancelOther               CancelReason = "other"
)

var allCancelReasons = []CancelReason{CancelChangedMind, CancelFoundOther, CancelNoResponse, CancelUnableToContact, CancelProviderUnavailable, CancelCustomerUnreachable, CancelOther}

func (c CancelReason) Valid() bool { return slices.Contains(allCancelReasons, c) }

// IssueReason is a problem reported on a match (no dispute workflow: it is
// recorded for operators).
type IssueReason string

var allIssueReasons = []IssueReason{"no_response", "unable_to_contact", "no_show", "price_dispute", "safety", "other"}

func (i IssueReason) Valid() bool { return slices.Contains(allIssueReasons, i) }

// NewRequestInput is what a customer sends.
type NewRequestInput struct {
	CustomerUserID uuid.UUID
	ClientID       *string
	Category       Category
	Description    *string
	VehicleInfo    *string
	ImageKey       *string
	Latitude       float64
	Longitude      float64
	LocationName   *string
	ContactPhone   string
}

func (in NewRequestInput) Validate() error {
	fields := map[string]string{}
	if in.ClientID != nil && (len(*in.ClientID) < 8 || len(*in.ClientID) > 64) {
		fields["client_id"] = "must be 8-64 characters"
	}
	if !in.Category.Valid() {
		fields["category"] = "must be a known service category"
	}
	if in.Description != nil && utf8.RuneCountInString(*in.Description) > 1000 {
		fields["description"] = "must be 1000 characters or fewer"
	}
	if in.VehicleInfo != nil && utf8.RuneCountInString(*in.VehicleInfo) > 120 {
		fields["vehicle_info"] = "must be 120 characters or fewer"
	}
	if in.ImageKey != nil && *in.ImageKey != "" && !ValidImageKey(*in.ImageKey) {
		fields["image_key"] = "must be a key returned by /uploads/presign"
	}
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		fields["latitude"] = "must be a valid latitude/longitude"
	}
	if in.LocationName != nil && utf8.RuneCountInString(*in.LocationName) > 200 {
		fields["location_name"] = "must be 200 characters or fewer"
	}
	if p := strings.TrimSpace(in.ContactPhone); len(p) < 3 || len(p) > 32 {
		fields["contact_phone"] = "must be 3-32 characters"
	}
	if len(fields) > 0 {
		return apperr.Validation("service request is invalid", fields)
	}
	return nil
}

// MaxOpenRequestsPerCustomer bounds how many requests one customer can
// have taking offers at once.
const MaxOpenRequestsPerCustomer = 3

// OfferStatus is an offer's lifecycle: pending → selected (customer's pick)
// → accepted (provider confirmed = match), or rejected/expired.
type OfferStatus string

const (
	OfferPending  OfferStatus = "pending"
	OfferSelected OfferStatus = "selected"
	OfferAccepted OfferStatus = "accepted"
	OfferRejected OfferStatus = "rejected"
	OfferExpired  OfferStatus = "expired"
)

// Offer is a provider's quote for a request. PriceTHB nil means the price
// is assessed on site ("ประเมินหน้างาน").
type Offer struct {
	ID         uuid.UUID
	RequestID  uuid.UUID
	ProviderID uuid.UUID
	PriceTHB   *int
	ETAMinutes int
	Note       *string
	Status     OfferStatus
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// EffectiveOffer is the offer's status as of now given its request: a
// selection that timed out is expired, and offers still pending on a
// request that closed (or expired) are expired too.
func EffectiveOffer(o Offer, r Request, now time.Time) OfferStatus {
	eff := r.Effective(now)
	switch o.Status {
	case OfferSelected:
		if eff != StatusPendingConfirmation {
			return OfferExpired
		}
	case OfferPending:
		if eff.IsClosed() {
			return OfferExpired
		}
	}
	return o.Status
}

// OfferInput is a provider's offer.
type OfferInput struct {
	PriceTHB   *int
	ETAMinutes int
	Note       *string
}

func (in OfferInput) Validate() error {
	fields := map[string]string{}
	if in.PriceTHB != nil && (*in.PriceTHB < 0 || *in.PriceTHB > 1_000_000) {
		fields["price_thb"] = "must be between 0 and 1000000 (or null for an on-site estimate)"
	}
	if in.ETAMinutes < 1 || in.ETAMinutes > 1440 {
		fields["eta_minutes"] = "must be between 1 and 1440"
	}
	if in.Note != nil && utf8.RuneCountInString(*in.Note) > 500 {
		fields["note"] = "must be 500 characters or fewer"
	}
	if len(fields) > 0 {
		return apperr.Validation("offer is invalid", fields)
	}
	return nil
}

// MatchStatus is the match's own lifecycle (the request carries the job
// progress).
type MatchStatus string

const (
	MatchActive    MatchStatus = "active"
	MatchCompleted MatchStatus = "completed"
	MatchCancelled MatchStatus = "cancelled"
)

// Match is a qualified match: the customer selected the offer and the
// provider accepted it. It is the billable event and unlocks contact.
type Match struct {
	ID              uuid.UUID
	RequestID       uuid.UUID
	OfferID         uuid.UUID
	ProviderID      uuid.UUID
	FeeCredits      int
	FeeWaived       bool
	Status          MatchStatus
	CancelledBy     *Role
	CancelReason    *string
	StartedTravelAt *time.Time
	CreatedAt       time.Time
	ClosedAt        *time.Time
}

// Charged reports whether credit was actually deducted for the match.
func (m Match) Charged() bool { return !m.FeeWaived && m.FeeCredits > 0 }

// RefundEligible is the cancellation refund rule: the customer cancelled
// shortly after the match (within grace) and before the provider started
// travelling, and the fee was actually charged. A provider backing out, or
// a cancellation after travel started, is never refunded here. The refund
// is a new REFUND ledger row; the original MATCH_FEE row is never touched.
func RefundEligible(m Match, requestStatus Status, cancelledBy Role, now time.Time, grace time.Duration) bool {
	return cancelledBy == RoleCustomer &&
		m.Status == MatchActive &&
		m.Charged() &&
		requestStatus == StatusMatched &&
		m.StartedTravelAt == nil &&
		now.Sub(m.CreatedAt) <= grace
}

// Event is one entry in a request's timeline.
type Event struct {
	Status    Status
	Actor     Role
	Note      *string
	CreatedAt time.Time
}

// Issue is a problem one matched party reported about the other.
type Issue struct {
	ID        uuid.UUID
	MatchID   uuid.UUID
	RequestID uuid.UUID
	Reporter  Role
	Reason    IssueReason
	Details   *string
	Status    string
	CreatedAt time.Time
}
