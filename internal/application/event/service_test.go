package event_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	appevent "floodnow-api/internal/application/event"
	"floodnow-api/internal/domain/apperr"
	domainevent "floodnow-api/internal/domain/event"
	"floodnow-api/internal/ports"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type memEvents struct {
	items map[uuid.UUID]domainevent.Event
}

func (m *memEvents) List(_ context.Context, f ports.EventFilter) ([]domainevent.Event, error) {
	var out []domainevent.Event
	for _, e := range m.items {
		if e.EndAt.After(f.Now) {
			out = append(out, e)
		}
	}
	return out, nil
}
func (m *memEvents) ListByOwner(_ context.Context, owner uuid.UUID, _ int) ([]domainevent.Event, error) {
	var out []domainevent.Event
	for _, e := range m.items {
		if e.OwnerUserID == owner {
			out = append(out, e)
		}
	}
	return out, nil
}
func (m *memEvents) CountUpcomingByOwner(_ context.Context, owner uuid.UUID, now time.Time) (int, error) {
	n := 0
	for _, e := range m.items {
		if e.OwnerUserID == owner && e.StoredStatus == domainevent.StoredActive && e.EndAt.After(now) {
			n++
		}
	}
	return n, nil
}
func (m *memEvents) Get(_ context.Context, id uuid.UUID) (*domainevent.Event, error) {
	if e, ok := m.items[id]; ok {
		return &e, nil
	}
	return nil, nil
}
func (m *memEvents) Create(_ context.Context, e *domainevent.Event) error {
	m.items[e.ID] = *e
	return nil
}
func (m *memEvents) Update(_ context.Context, e *domainevent.Event) error {
	m.items[e.ID] = *e
	return nil
}
func (m *memEvents) Delete(_ context.Context, id uuid.UUID) error { delete(m.items, id); return nil }

var (
	t0    = time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	owner = uuid.New()
	other = uuid.New()
)

func ptr[T any](v T) *T { return &v }

func templeFair() domainevent.Fields {
	return domainevent.Fields{
		Title: ptr("  งานวัดประจำปี  "), Category: ptr(domainevent.CategoryTempleFair),
		Latitude: ptr(13.75), Longitude: ptr(100.5),
		StartAt: ptr(t0.Add(24 * time.Hour)), EndAt: ptr(t0.Add(72 * time.Hour)),
	}
}

func newService() (*appevent.Service, *memEvents, *fakeClock) {
	repo := &memEvents{items: map[uuid.UUID]domainevent.Event{}}
	clock := &fakeClock{now: t0}
	return appevent.NewService(repo, clock), repo, clock
}

func assertCode(t *testing.T, err error, code apperr.Code) {
	t.Helper()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestCreateValidatesAndOwnsEvent(t *testing.T) {
	svc, _, _ := newService()
	e, err := svc.Create(context.Background(), owner, templeFair())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Title != "งานวัดประจำปี" || e.OwnerUserID != owner || e.Status(t0) != domainevent.StatusActive {
		t.Errorf("unexpected event: %+v", e)
	}

	bad := templeFair()
	bad.EndAt = ptr(t0.Add(12 * time.Hour)) // before start
	_, err = svc.Create(context.Background(), owner, bad)
	assertCode(t, err, apperr.CodeValidation)

	past := templeFair()
	past.StartAt, past.EndAt = ptr(t0.Add(-3*time.Hour)), ptr(t0.Add(-time.Hour))
	_, err = svc.Create(context.Background(), owner, past)
	assertCode(t, err, apperr.CodeValidation)

	missing := templeFair()
	missing.Title = nil
	_, err = svc.Create(context.Background(), owner, missing)
	assertCode(t, err, apperr.CodeValidation)

	foreignKey := templeFair()
	foreignKey.ImageKey = ptr("announcements/2026/09/28/x.jpg")
	_, err = svc.Create(context.Background(), owner, foreignKey)
	assertCode(t, err, apperr.CodeValidation)
}

func TestOnlyTheOwnerCanUpdateCancelOrDelete(t *testing.T) {
	svc, repo, _ := newService()
	e, _ := svc.Create(context.Background(), owner, templeFair())

	_, err := svc.Update(context.Background(), other, e.ID, domainevent.Fields{Title: ptr("hijacked")})
	assertCode(t, err, apperr.CodeForbidden)
	_, err = svc.Cancel(context.Background(), other, e.ID)
	assertCode(t, err, apperr.CodeForbidden)
	assertCode(t, svc.Delete(context.Background(), other, e.ID), apperr.CodeForbidden)
	if repo.items[e.ID].Title != "งานวัดประจำปี" {
		t.Fatal("a non-owner changed the event")
	}

	updated, err := svc.Update(context.Background(), owner, e.ID, domainevent.Fields{Title: ptr("ตลาดนัดคลองถม"), Category: ptr(domainevent.CategoryMarket)})
	if err != nil || updated.Title != "ตลาดนัดคลองถม" || updated.Category != domainevent.CategoryMarket {
		t.Fatalf("owner update failed: %v %+v", err, updated)
	}

	cancelled, err := svc.Cancel(context.Background(), owner, e.ID)
	if err != nil || cancelled.Status(t0) != domainevent.StatusCancelled {
		t.Fatalf("cancel failed: %v", err)
	}
	_, err = svc.Update(context.Background(), owner, e.ID, domainevent.Fields{Title: ptr("again")})
	assertCode(t, err, apperr.CodeConflict)

	if err := svc.Delete(context.Background(), owner, e.ID); err != nil {
		t.Fatalf("owner delete failed: %v", err)
	}
	_, err = svc.Get(context.Background(), e.ID)
	assertCode(t, err, apperr.CodeNotFound)
}

func TestEndedEventsAreNotActiveOrListed(t *testing.T) {
	svc, _, clock := newService()
	e, _ := svc.Create(context.Background(), owner, templeFair())

	clock.now = t0.Add(73 * time.Hour)
	got, _ := svc.Get(context.Background(), e.ID)
	if got.Status(clock.now) != domainevent.StatusEnded {
		t.Errorf("status after end = %s, want ended", got.Status(clock.now))
	}
	listed, _ := svc.List(context.Background(), nil, 0)
	if len(listed) != 0 {
		t.Errorf("ended event still listed")
	}
	_, err := svc.Update(context.Background(), owner, e.ID, domainevent.Fields{Title: ptr("late")})
	assertCode(t, err, apperr.CodeConflict)
}

func TestUpcomingEventsPerOwnerAreCapped(t *testing.T) {
	svc, _, _ := newService()
	for i := 0; i < domainevent.MaxUpcomingPerOwner; i++ {
		if _, err := svc.Create(context.Background(), owner, templeFair()); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	_, err := svc.Create(context.Background(), owner, templeFair())
	assertCode(t, err, apperr.CodeConflict)
	if _, err := svc.Create(context.Background(), other, templeFair()); err != nil {
		t.Errorf("another user is not capped by the first: %v", err)
	}
}
