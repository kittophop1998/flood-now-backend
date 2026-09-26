// Package officialflood serves the official (GISTDA) flood-area map layer
// from an in-memory cache: each period is fetched from the provider at most
// once per TTL no matter how many clients pan the map, concurrent misses
// share one upstream call, a failure backs off before the next try, and the
// last good copy is served (marked stale) while the provider is down.
package officialflood

import (
	"context"
	"log"
	"sync"
	"time"

	"floodnow-api/internal/domain/apperr"
	domain "floodnow-api/internal/domain/officialflood"
	"floodnow-api/internal/ports"
)

const (
	// After a failed fetch, wait this long before asking the provider again
	// (requests in between get the stale copy or UNAVAILABLE).
	failureBackoff = time.Minute
	// Never present a copy older than this, even as stale.
	maxStaleAge = 24 * time.Hour
	// Upper bound for one refresh, independent of the request that started
	// it (a timed-out client doesn't cancel the shared fetch).
	refreshTimeout = 60 * time.Second
)

var clipLimits = domain.ClipLimits{MaxAreas: 5000, MaxVertices: 250_000}

type Service struct {
	provider ports.FloodAreaProvider
	clock    ports.Clock
	ttl      time.Duration

	mu      sync.Mutex
	entries map[domain.Period]*entry
}

type entry struct {
	snap     *domain.Snapshot
	failedAt time.Time
	inflight chan struct{} // closed when the running refresh finishes
}

func NewService(provider ports.FloodAreaProvider, clock ports.Clock, ttl time.Duration) *Service {
	return &Service{provider: provider, clock: clock, ttl: ttl, entries: map[domain.Period]*entry{}}
}

// Layer is the flood layer for one period and viewport.
type Layer struct {
	Period     domain.Period
	ObservedAt *time.Time
	FetchedAt  time.Time
	// Stale means the provider couldn't be reached and this is the last
	// successful copy (FetchedAt says how old).
	Stale   bool
	Areas   []domain.ClippedArea
	HasMore bool
}

// Get returns the period's flood areas inside view (all of them when view
// is nil), simplified for the view's scale.
func (s *Service) Get(ctx context.Context, period domain.Period, view *domain.Bounds) (*Layer, error) {
	if !period.Valid() {
		return nil, apperr.Validation("period is invalid", map[string]string{"period": "must be one of 1d, 3d, 7d, 30d"})
	}
	missLogged := false
	for {
		s.mu.Lock()
		e := s.entries[period]
		if e == nil {
			e = &entry{}
			s.entries[period] = e
		}
		now := s.clock.Now()
		if e.snap != nil && now.Sub(e.snap.FetchedAt) < s.ttl {
			snap := e.snap
			s.mu.Unlock()
			if !missLogged {
				log.Printf("official flood: provider=GISTDA period=%s cache=hit", period)
			}
			return present(snap, false, view), nil
		}
		if e.inflight == nil {
			if !e.failedAt.IsZero() && now.Sub(e.failedAt) < failureBackoff {
				snap := e.snap
				s.mu.Unlock()
				return fallback(period, snap, now, view)
			}
			e.inflight = make(chan struct{})
			go s.refresh(period, e, e.inflight)
		}
		if !missLogged {
			log.Printf("official flood: provider=GISTDA period=%s cache=miss", period)
			missLogged = true
		}
		done, snap := e.inflight, e.snap
		s.mu.Unlock()

		select {
		case <-done:
			// Re-evaluate: fresh copy, or the failure/backoff path.
		case <-ctx.Done():
			return fallback(period, snap, s.clock.Now(), view)
		}
	}
}

func (s *Service) refresh(period domain.Period, e *entry, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	snap, err := s.provider.FloodAreas(ctx, period)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		log.Printf("official flood: provider=GISTDA period=%s refresh failed: %v", period, err)
		e.failedAt = s.clock.Now()
	} else {
		e.snap = snap
		e.failedAt = time.Time{}
	}
	e.inflight = nil
	close(done)
}

// fallback serves the last good copy as stale, or UNAVAILABLE without one.
func fallback(period domain.Period, snap *domain.Snapshot, now time.Time, view *domain.Bounds) (*Layer, error) {
	if snap != nil && now.Sub(snap.FetchedAt) < maxStaleAge {
		log.Printf("official flood: provider=GISTDA period=%s serving stale copy from %s", period, snap.FetchedAt.Format(time.RFC3339))
		return present(snap, true, view), nil
	}
	return nil, apperr.Unavailable("official flood data is unavailable right now")
}

func present(snap *domain.Snapshot, stale bool, view *domain.Bounds) *Layer {
	var v domain.Bounds
	if view != nil {
		v = *view
	} else if ext, ok := snap.Extent(); ok {
		v = ext
	}
	areas, more := domain.Clip(snap.Areas, v, domain.ToleranceFor(v), clipLimits)
	return &Layer{
		Period:     snap.Period,
		ObservedAt: snap.ObservedAt,
		FetchedAt:  snap.FetchedAt,
		Stale:      stale,
		Areas:      areas,
		HasMore:    more,
	}
}
