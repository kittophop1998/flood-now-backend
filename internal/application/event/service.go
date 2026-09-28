// Package event contains the community-event use cases. Anyone may read
// events; only a signed-in user may create one, and only its owner may edit,
// cancel or delete it (checked here, never trusted from the client).
package event

import (
	"context"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	domainevent "floodnow-api/internal/domain/event"
	"floodnow-api/internal/ports"
)

const (
	defaultListLimit = 200
	maxListLimit     = 500
	mineLimit        = 50
)

type Service struct {
	repo  ports.EventRepository
	clock ports.Clock
}

func NewService(repo ports.EventRepository, clock ports.Clock) *Service {
	return &Service{repo: repo, clock: clock}
}

// List returns events that haven't ended (active or cancelled — a cancelled
// event stays visible, labelled, until its end), soonest first.
func (s *Service) List(ctx context.Context, bbox *ports.BBox, limit int) ([]domainevent.Event, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return s.repo.List(ctx, ports.EventFilter{BBox: bbox, Now: s.clock.Now(), Limit: limit})
}

// Get returns any event by id (including ended/cancelled ones).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*domainevent.Event, error) {
	e, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, apperr.NotFound("event not found")
	}
	return e, nil
}

// Mine lists the user's own events, newest first.
func (s *Service) Mine(ctx context.Context, userID uuid.UUID) ([]domainevent.Event, error) {
	return s.repo.ListByOwner(ctx, userID, mineLimit)
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, in domainevent.Fields) (*domainevent.Event, error) {
	now := s.clock.Now()
	if err := in.ValidateCreate(now); err != nil {
		return nil, err
	}
	n, err := s.repo.CountUpcomingByOwner(ctx, userID, now)
	if err != nil {
		return nil, err
	}
	if n >= domainevent.MaxUpcomingPerOwner {
		return nil, apperr.Conflict("too many upcoming events; cancel or delete one first")
	}
	e := &domainevent.Event{
		ID:           uuid.New(),
		OwnerUserID:  userID,
		StoredStatus: domainevent.StoredActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := in.Apply(e, now); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, e); err != nil {
		return nil, err
	}
	return s.Get(ctx, e.ID)
}

// Update edits the user's own event. A cancelled or ended event can't be
// edited any more (CONFLICT).
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, in domainevent.Fields) (*domainevent.Event, error) {
	e, err := s.owned(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	if e.Status(now) != domainevent.StatusActive {
		return nil, apperr.Conflict("a cancelled or ended event can't be edited")
	}
	if err := in.Apply(e, now); err != nil {
		return nil, err
	}
	e.UpdatedAt = now
	if err := s.repo.Update(ctx, e); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Cancel marks the user's own event cancelled (it stays visible, labelled,
// until it would have ended). Cancelling twice is a no-op.
func (s *Service) Cancel(ctx context.Context, userID, id uuid.UUID) (*domainevent.Event, error) {
	e, err := s.owned(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if e.StoredStatus == domainevent.StoredCancelled {
		return e, nil
	}
	now := s.clock.Now()
	e.StoredStatus = domainevent.StoredCancelled
	e.CancelledAt = &now
	e.UpdatedAt = now
	if err := s.repo.Update(ctx, e); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Delete removes the user's own event entirely.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	if _, err := s.owned(ctx, userID, id); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}

// owned loads an event and checks the caller owns it: unknown is
// NOT_FOUND, someone else's is FORBIDDEN (events are public, so their
// existence isn't a secret).
func (s *Service) owned(ctx context.Context, userID, id uuid.UUID) (*domainevent.Event, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !e.IsOwnedBy(userID) {
		return nil, apperr.Forbidden("only the event's organizer can change it")
	}
	return e, nil
}
