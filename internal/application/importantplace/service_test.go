package importantplace_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	appplace "floodnow-api/internal/application/importantplace"
	"floodnow-api/internal/domain/apperr"
	domainplace "floodnow-api/internal/domain/importantplace"
	"floodnow-api/internal/ports"
)

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

type fakePlaces struct{ places []domainplace.Place }

func (f *fakePlaces) List(ctx context.Context, filter ports.ImportantPlaceFilter) ([]domainplace.Place, error) {
	return f.places, nil
}

func (f *fakePlaces) Get(ctx context.Context, id uuid.UUID) (*domainplace.Place, error) {
	for _, p := range f.places {
		if p.ID == id {
			cp := p
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakePlaces) Create(ctx context.Context, p *domainplace.Place) error {
	f.places = append(f.places, *p)
	return nil
}

func (f *fakePlaces) Update(ctx context.Context, p *domainplace.Place) error {
	for i := range f.places {
		if f.places[i].ID == p.ID {
			f.places[i] = *p
		}
	}
	return nil
}

func (f *fakePlaces) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	for i, p := range f.places {
		if p.ID == id {
			f.places = append(f.places[:i], f.places[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakePlaces) CountByDeviceSince(ctx context.Context, deviceID string, since time.Time) (int, error) {
	n := 0
	for _, p := range f.places {
		if p.OwnedBy(deviceID) && !p.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

const device = "device-aaaaaaaa"

var now = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func validFields() domainplace.Fields {
	return domainplace.Fields{
		Name: ptr("Temple shelter"), Category: ptr(domainplace.CategoryShelter),
		Latitude: ptr(13.75), Longitude: ptr(100.5), Source: ptr("should be dropped"),
	}
}

func hasCode(err error, code apperr.Code) bool {
	var ae *apperr.Error
	return errors.As(err, &ae) && ae.Code == code
}

func TestCreateCommunityMarksOwnerAndDropsSource(t *testing.T) {
	repo := &fakePlaces{}
	svc := appplace.NewService(repo, fakeClock{now})
	p, err := svc.CreateCommunity(context.Background(), device, validFields())
	if err != nil {
		t.Fatal(err)
	}
	if p.Origin() != domainplace.OriginCommunity || !p.OwnedBy(device) {
		t.Fatalf("want community place owned by device, got origin=%s", p.Origin())
	}
	if p.Source != nil {
		t.Fatalf("community place must not carry a source, got %q", *p.Source)
	}
	if p.Status != domainplace.StatusUnknown {
		t.Fatalf("default status = %s, want unknown", p.Status)
	}
}

func TestCreateCommunityValidates(t *testing.T) {
	svc := appplace.NewService(&fakePlaces{}, fakeClock{now})
	if _, err := svc.CreateCommunity(context.Background(), "short", validFields()); !hasCode(err, apperr.CodeValidation) {
		t.Fatalf("bad device_id: want validation error, got %v", err)
	}
	f := validFields()
	f.Name = nil
	if _, err := svc.CreateCommunity(context.Background(), device, f); !hasCode(err, apperr.CodeValidation) {
		t.Fatalf("missing name: want validation error, got %v", err)
	}
}

func TestCreateCommunityRateLimited(t *testing.T) {
	repo := &fakePlaces{}
	for i := 0; i < appplace.MaxCommunityPerDevicePerDay; i++ {
		repo.places = append(repo.places, domainplace.Place{ID: uuid.New(), CreatedByDevice: ptr(device), CreatedAt: now.Add(-time.Hour)})
	}
	// Older than a day doesn't count.
	repo.places[0].CreatedAt = now.Add(-25 * time.Hour)
	svc := appplace.NewService(repo, fakeClock{now})
	if _, err := svc.CreateCommunity(context.Background(), device, validFields()); err != nil {
		t.Fatalf("under the limit: %v", err)
	}
	if _, err := svc.CreateCommunity(context.Background(), device, validFields()); !hasCode(err, apperr.CodeRateLimited) {
		t.Fatalf("over the limit: want rate limited, got %v", err)
	}
}

func TestOnlyOwnerCanChangeCommunityPlace(t *testing.T) {
	official := domainplace.Place{ID: uuid.New(), Name: "Hospital", Status: domainplace.StatusOpen}
	mine := domainplace.Place{ID: uuid.New(), Name: "Boat", Status: domainplace.StatusOpen, CreatedByDevice: ptr(device)}
	repo := &fakePlaces{places: []domainplace.Place{official, mine}}
	svc := appplace.NewService(repo, fakeClock{now})
	ctx := context.Background()
	closed := domainplace.Fields{Status: ptr(domainplace.StatusClosed)}

	if _, err := svc.UpdateOwn(ctx, device, official.ID, closed); !hasCode(err, apperr.CodeNotFound) {
		t.Fatalf("editing an official place: want not found, got %v", err)
	}
	if _, err := svc.UpdateOwn(ctx, "other-device-1", mine.ID, closed); !hasCode(err, apperr.CodeNotFound) {
		t.Fatalf("editing someone else's place: want not found, got %v", err)
	}
	if err := svc.DeleteOwn(ctx, "other-device-1", mine.ID); !hasCode(err, apperr.CodeNotFound) {
		t.Fatalf("deleting someone else's place: want not found, got %v", err)
	}
	p, err := svc.UpdateOwn(ctx, device, mine.ID, closed)
	if err != nil || p.Status != domainplace.StatusClosed {
		t.Fatalf("owner update: %v %+v", err, p)
	}
	if err := svc.DeleteOwn(ctx, device, mine.ID); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	if len(repo.places) != 1 {
		t.Fatalf("want 1 place left, got %d", len(repo.places))
	}
}
