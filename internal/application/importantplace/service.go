// Package importantplace contains the important-places layer use cases:
// public viewport reads and operator CRUD.
package importantplace

import (
	"context"
	"log"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	domainplace "floodnow-api/internal/domain/importantplace"
	"floodnow-api/internal/ports"
)

const (
	DefaultListLimit = 300
	MaxListLimit     = 500
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
