package report

import (
	"fmt"
	"time"
)

// Status is a report's lifecycle state. It is never stored: it is derived
// from the stored timestamps (stale_at, expires_at, resolved_at) so it can't
// drift, and the same rule is used for API responses and list filtering.
type Status string

const (
	StatusActive        Status = "active"
	StatusPossiblyStale Status = "possibly_stale"
	StatusResolved      Status = "resolved"
	StatusExpired       Status = "expired"
)

func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusPossiblyStale, StatusResolved, StatusExpired:
		return true
	}
	return false
}

// DefaultVisibleStatuses is what the map shows unless the caller asks
// otherwise: resolved and expired reports don't pollute the default view.
var DefaultVisibleStatuses = []Status{StatusActive, StatusPossiblyStale}

// Status derives the lifecycle state at now. Resolution wins over time-based
// states so a resolved report never reads as merely "stale".
func (r Report) Status(now time.Time) Status {
	switch {
	case r.ResolvedAt != nil:
		return StatusResolved
	case !now.Before(r.ExpiresAt):
		return StatusExpired
	case !now.Before(r.StaleAt):
		return StatusPossiblyStale
	default:
		return StatusActive
	}
}

// FreshnessPolicy is the single, configurable source of lifecycle timing.
// Road/flood conditions change fast; facilities (shelters, aid points) are
// expected to stay put much longer, so they get their own window.
type FreshnessPolicy struct {
	StaleAfter         time.Duration // no confirmation for this long → possibly_stale
	TTL                time.Duration // no confirmation for this long → expired
	FacilityStaleAfter time.Duration
	FacilityTTL        time.Duration
	// ResolveThreshold is how many "cleared" votes are needed (and they must
	// outnumber "still active" votes) before a report counts as resolved, so
	// one anonymous tap can't hide a report from everyone.
	ResolveThreshold int
}

func (p FreshnessPolicy) Validate() error {
	if p.StaleAfter <= 0 || p.TTL <= 0 || p.FacilityStaleAfter <= 0 || p.FacilityTTL <= 0 {
		return fmt.Errorf("freshness durations must be positive")
	}
	if p.StaleAfter > p.TTL || p.FacilityStaleAfter > p.FacilityTTL {
		return fmt.Errorf("stale-after must not exceed TTL")
	}
	if p.ResolveThreshold < 1 {
		return fmt.Errorf("resolve threshold must be >= 1")
	}
	return nil
}

// Window returns when a report of type t, verified at verifiedAt, becomes
// possibly stale and when it expires.
func (p FreshnessPolicy) Window(t Type, verifiedAt time.Time) (staleAt, expiresAt time.Time) {
	if t.IsFacility() {
		return verifiedAt.Add(p.FacilityStaleAfter), verifiedAt.Add(p.FacilityTTL)
	}
	return verifiedAt.Add(p.StaleAfter), verifiedAt.Add(p.TTL)
}

// NextResolvedAt applies the resolution rule after a confirmation changes the
// vote counts: resolved once cleared votes reach the threshold and outnumber
// still-active votes; re-opened (nil) if that stops being true. An already
// resolved report keeps its original resolved_at.
func (p FreshnessPolicy) NextResolvedAt(current *time.Time, stillActive, cleared int, now time.Time) *time.Time {
	if cleared >= p.ResolveThreshold && cleared > stillActive {
		if current != nil {
			return current
		}
		return &now
	}
	return nil
}

// EventKind is an entry in a report's append-only history. Events feed the
// in-app notifications and are the hook for future push delivery.
type EventKind string

const (
	EventCreated   EventKind = "created"
	EventConfirmed EventKind = "confirmed" // a still_active confirmation
	EventResolved  EventKind = "resolved"
	EventReopened  EventKind = "reopened"
)

// ResolutionEvent returns the event emitted when resolved_at moves from
// before to after, or "" when the resolution state didn't change.
func ResolutionEvent(before, after *time.Time) EventKind {
	switch {
	case before == nil && after != nil:
		return EventResolved
	case before != nil && after == nil:
		return EventReopened
	}
	return ""
}
