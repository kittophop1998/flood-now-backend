// Package importantplace contains the important-places layer use cases:
// public viewport reads, places added by anyone from the app, and operator
// CRUD.
package importantplace

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	domainplace "floodnow-api/internal/domain/importantplace"
	"floodnow-api/internal/ports"
)

const (
	DefaultListLimit = 300
	MaxListLimit     = 500
	// MaxCommunityPerDevicePerDay caps how many places one device can add.
	MaxCommunityPerDevicePerDay = 10
)

type Service struct {
	repo  ports.ImportantPlaceRepository
	clock ports.Clock
}

func NewService(repo ports.ImportantPlaceRepository, clock ports.Clock) *Service {
	return &Service{repo: repo, clock: clock}
}

type ListInput struct {
	BBox       *ports.BBox
	Categories []domainplace.Category
	Statuses   []domainplace.Status
	Limit      int
}

type ListResult struct {
	Places  []domainplace.Place
	HasMore bool
}

// List returns places in a viewport; a bbox is required so the layer never
// loads every place at once.
func (s *Service) List(ctx context.Context, in ListInput) (*ListResult, error) {
	if in.BBox == nil {
		return nil, apperr.Validation("important places query is invalid", map[string]string{"bbox": "min_lat, max_lat, min_lng, max_lng are required"})
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	places, err := s.repo.List(ctx, ports.ImportantPlaceFilter{BBox: in.BBox, Categories: in.Categories, Statuses: in.Statuses, Limit: limit + 1})
	if err != nil {
		return nil, err
	}
	res := &ListResult{Places: places}
	if len(places) > limit {
		res.Places, res.HasMore = places[:limit], true
	}
	return res, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*domainplace.Place, error) {
	p, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperr.NotFound("important place not found")
	}
	return p, nil
}

func (s *Service) Create(ctx context.Context, in domainplace.Fields) (*domainplace.Place, error) {
	if err := in.Validate(true); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	p := &domainplace.Place{ID: uuid.New(), Status: domainplace.StatusUnknown, CreatedAt: now, UpdatedAt: now}
	in.Apply(p)
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	log.Printf("admin: important place %s created", p.ID)
	return p, nil
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, in domainplace.Fields) (*domainplace.Place, error) {
	if err := in.Validate(false); err != nil {
		return nil, err
	}
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	in.Apply(p)
	p.UpdatedAt = s.clock.Now()
	if err := s.repo.Update(ctx, p); err != nil {
		return nil, err
	}
	log.Printf("admin: important place %s updated", p.ID)
	return p, nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	ok, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("important place not found")
	}
	log.Printf("admin: important place %s deleted", id)
	return nil
}

// CreateCommunity adds a place on behalf of an anonymous device. It is shown
// as community-added (not curated); only that device or an operator can
// change it. Community places carry no source.
func (s *Service) CreateCommunity(ctx context.Context, deviceID string, in domainplace.Fields) (*domainplace.Place, error) {
	if err := domainplace.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	in.Source = nil
	if err := in.Validate(true); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	n, err := s.repo.CountByDeviceSince(ctx, deviceID, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	if n >= MaxCommunityPerDevicePerDay {
		return nil, apperr.RateLimited("too many places added from this device; try again later")
	}
	p := &domainplace.Place{ID: uuid.New(), Status: domainplace.StatusUnknown, CreatedByDevice: &deviceID, CreatedAt: now, UpdatedAt: now}
	in.Apply(p)
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// own loads a place the device added; anything else is "not found" for it.
func (s *Service) own(ctx context.Context, deviceID string, id uuid.UUID) (*domainplace.Place, error) {
	if err := domainplace.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	p, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil || !p.OwnedBy(deviceID) {
		return nil, apperr.NotFound("important place not found")
	}
	return p, nil
}

func (s *Service) UpdateOwn(ctx context.Context, deviceID string, id uuid.UUID, in domainplace.Fields) (*domainplace.Place, error) {
	in.Source = nil
	if err := in.Validate(false); err != nil {
		return nil, err
	}
	p, err := s.own(ctx, deviceID, id)
	if err != nil {
		return nil, err
	}
	in.Apply(p)
	p.UpdatedAt = s.clock.Now()
	if err := s.repo.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) DeleteOwn(ctx context.Context, deviceID string, id uuid.UUID) error {
	if _, err := s.own(ctx, deviceID, id); err != nil {
		return err
	}
	if _, err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return nil
}
