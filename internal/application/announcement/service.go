// Package announcement contains the official-announcement use cases: public
// reads of published announcements and operator create/update/publish.
package announcement

import (
	"context"
	"log"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/announcement"
	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/ports"
)

const (
	publicListLimit = 50
	adminListLimit  = 200
)

type Service struct {
	repo  ports.AnnouncementRepository
	clock ports.Clock
}

func NewService(repo ports.AnnouncementRepository, clock ports.Clock) *Service {
	return &Service{repo: repo, clock: clock}
}

// List returns published announcements: live ones, plus those that ended in
// the last week when includeExpired. bbox (optional) keeps those affecting
// the viewport and those without a location.
func (s *Service) List(ctx context.Context, bbox *ports.BBox, includeExpired bool) ([]announcement.Announcement, error) {
	now := s.clock.Now()
	return s.repo.List(ctx, ports.AnnouncementFilter{
		Now:            now,
		IncludeExpired: includeExpired,
		ExpiredSince:   now.Add(-announcement.ExpiredLookback),
		BBox:           bbox,
		Limit:          publicListLimit,
	})
}

// Get returns a published announcement; drafts are not found publicly.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*announcement.Announcement, error) {
	a, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if a == nil || a.PublishedAt == nil {
		return nil, apperr.NotFound("announcement not found")
	}
	return a, nil
}

// AdminList returns every announcement including drafts and old ones.
func (s *Service) AdminList(ctx context.Context) ([]announcement.Announcement, error) {
	return s.repo.List(ctx, ports.AnnouncementFilter{Now: s.clock.Now(), IncludeDrafts: true, IncludeExpired: true, Limit: adminListLimit})
}

func (s *Service) adminGet(ctx context.Context, id uuid.UUID) (*announcement.Announcement, error) {
	a, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apperr.NotFound("announcement not found")
	}
	return a, nil
}

// Create stores a draft; it becomes public only once published.
func (s *Service) Create(ctx context.Context, in announcement.Fields, publish bool) (*announcement.Announcement, error) {
	if err := in.Validate(true); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	a := &announcement.Announcement{ID: uuid.New(), CreatedAt: now, UpdatedAt: now}
	if err := in.Apply(a); err != nil {
		return nil, err
	}
	if publish {
		a.PublishedAt = &now
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	log.Printf("admin: announcement %s created (published=%v)", a.ID, publish)
	return a, nil
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, in announcement.Fields) (*announcement.Announcement, error) {
	if err := in.Validate(false); err != nil {
		return nil, err
	}
	a, err := s.adminGet(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := in.Apply(a); err != nil {
		return nil, err
	}
	a.UpdatedAt = s.clock.Now()
	if err := s.repo.Update(ctx, a); err != nil {
		return nil, err
	}
	log.Printf("admin: announcement %s updated", a.ID)
	return a, nil
}

// SetPublished publishes (visible from starts_at) or withdraws an announcement.
func (s *Service) SetPublished(ctx context.Context, id uuid.UUID, publish bool) (*announcement.Announcement, error) {
	a, err := s.adminGet(ctx, id)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	switch {
	case publish && a.PublishedAt == nil:
		a.PublishedAt = &now
	case !publish:
		a.PublishedAt = nil
	}
	a.UpdatedAt = now
	if err := s.repo.Update(ctx, a); err != nil {
		return nil, err
	}
	log.Printf("admin: announcement %s published=%v", a.ID, publish)
	return a, nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	ok, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("announcement not found")
	}
	log.Printf("admin: announcement %s deleted", id)
	return nil
}
