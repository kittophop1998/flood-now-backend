// Package cctv serves the official highway-camera map layer from an
// in-memory copy of the provider's catalog: it's fetched at most once per
// TTL no matter how many clients pan the map, concurrent misses share one
// upstream call, a failure backs off before the next try, and the last good
// copy is served (marked stale) while the provider is down. Only camera
// metadata is handled here — FloodNow never fetches camera images.
package cctv

import (
	"context"
	"log"
	"sync"
	"time"

	"floodnow-api/internal/domain/apperr"
	domain "floodnow-api/internal/domain/cctv"
	"floodnow-api/internal/ports"
)

const (
	failureBackoff = time.Minute
	maxStaleAge    = 24 * time.Hour
	refreshTimeout = 45 * time.Second

	DefaultLimit  = 300
	MaxLimit      = 1000
	DefaultRadius = 1000.0
	MaxRadius     = 5000.0
	DefaultNear   = 3
	MaxNear       = 10
)

type Service struct {
	provider ports.CCTVProvider
	clock    ports.Clock
	ttl      time.Duration

	mu       sync.Mutex
	catalog  *domain.Catalog
	failedAt time.Time
	inflight chan struct{}
}

func NewService(provider ports.CCTVProvider, clock ports.Clock, ttl time.Duration) *Service {
	return &Service{provider: provider, clock: clock, ttl: ttl}
}

// Snapshot is the catalog a response was built from.
type Snapshot struct {
	FetchedAt time.Time
	// Stale means the provider couldn't be reached and this is the last
	// successful copy (FetchedAt says how old).
	Stale bool
}

type ListResult struct {
	Snapshot
	Cameras []domain.Camera
	HasMore bool
}

// List returns cameras inside view (all when nil), at most limit
// (default DefaultLimit, capped at MaxLimit).
func (s *Service) List(ctx context.Context, view *domain.Bounds, limit int) (*ListResult, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	cat, snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	cams, more := cat.Within(view, limit)
	return &ListResult{Snapshot: snap, Cameras: cams, HasMore: more}, nil
}

type GetResult struct {
	Snapshot
	Camera domain.Camera
}

func (s *Service) Get(ctx context.Context, id string) (*GetResult, error) {
	cat, snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	cam, ok := cat.Find(id)
	if !ok {
		return nil, apperr.NotFound("camera not found")
	}
	return &GetResult{Snapshot: snap, Camera: cam}, nil
}

type NearbyInput struct {
	Latitude, Longitude float64
	RadiusM             float64
	Limit               int
}

type NearbyResult struct {
	Snapshot
	Cameras []domain.Near
}

// Nearby returns the closest cameras within the radius (default 1 km,
// max 5 km), at most limit (default 3, max 10).
func (s *Service) Nearby(ctx context.Context, in NearbyInput) (*NearbyResult, error) {
	fields := map[string]string{}
	if in.Latitude < -90 || in.Latitude > 90 {
		fields["lat"] = "must be between -90 and 90"
	}
	if in.Longitude < -180 || in.Longitude > 180 {
		fields["lng"] = "must be between -180 and 180"
	}
	if in.RadiusM < 0 || in.RadiusM > MaxRadius {
		fields["radius_m"] = "must be between 0 and 5000"
	}
	if in.Limit < 0 || in.Limit > MaxNear {
		fields["limit"] = "must be between 1 and 10"
	}
	if len(fields) > 0 {
		return nil, apperr.Validation("nearby camera query is invalid", fields)
	}
	if in.RadiusM == 0 {
		in.RadiusM = DefaultRadius
	}
	if in.Limit == 0 {
		in.Limit = DefaultNear
	}
	cat, snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return &NearbyResult{Snapshot: snap, Cameras: cat.Nearest(in.Latitude, in.Longitude, in.RadiusM, in.Limit)}, nil
}

// load returns the cached catalog, refreshing it when older than the TTL.
func (s *Service) load(ctx context.Context) (*domain.Catalog, Snapshot, error) {
	missLogged := false
	for {
		s.mu.Lock()
		now := s.clock.Now()
		if s.catalog != nil && now.Sub(s.catalog.FetchedAt) < s.ttl {
			cat := s.catalog
			s.mu.Unlock()
			return cat, Snapshot{FetchedAt: cat.FetchedAt}, nil
		}
		if s.inflight == nil {
			if !s.failedAt.IsZero() && now.Sub(s.failedAt) < failureBackoff {
				cat := s.catalog
				s.mu.Unlock()
				return fallback(cat, now)
			}
			s.inflight = make(chan struct{})
			go s.refresh(s.inflight)
		}
		if !missLogged {
			log.Printf("cctv: provider=DOH cache=miss")
			missLogged = true
		}
		done, cat := s.inflight, s.catalog
		s.mu.Unlock()

		select {
		case <-done:
			// Re-evaluate: fresh copy, or the failure/backoff path.
		case <-ctx.Done():
			return fallback(cat, s.clock.Now())
		}
	}
}

func (s *Service) refresh(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	cat, err := s.provider.Cameras(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		log.Printf("cctv: provider=DOH refresh failed: %v", err)
		s.failedAt = s.clock.Now()
	} else {
		s.catalog = cat
		s.failedAt = time.Time{}
	}
	s.inflight = nil
	close(done)
}

// fallback serves the last good copy as stale, or UNAVAILABLE without one.
func fallback(cat *domain.Catalog, now time.Time) (*domain.Catalog, Snapshot, error) {
	if cat != nil && now.Sub(cat.FetchedAt) < maxStaleAge {
		log.Printf("cctv: provider=DOH serving stale catalog from %s", cat.FetchedAt.Format(time.RFC3339))
		return cat, Snapshot{FetchedAt: cat.FetchedAt, Stale: true}, nil
	}
	return nil, Snapshot{}, apperr.Unavailable("highway camera data is unavailable right now")
}
