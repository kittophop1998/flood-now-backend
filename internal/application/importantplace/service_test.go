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

var now = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func hasCode(err error, code apperr.Code) bool {
	var ae *apperr.Error
	return errors.As(err, &ae) && ae.Code == code
}

func TestCreateIsOfficialAndKeepsSource(t *testing.T) {
	svc := appplace.NewService(&fakePlaces{}, fakeClock{now})
	p, err := svc.Create(context.Background(), domainplace.Fields{
		Name: ptr("Temple shelter"), Category: ptr(domainplace.CategoryShelter),
		Latitude: ptr(13.75), Longitude: ptr(100.5), Source: ptr("District office"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Origin() != domainplace.OriginOfficial {
		t.Fatalf("origin = %s, want official", p.Origin())
	}
	if p.Source == nil || *p.Source != "District office" {
		t.Fatalf("source not kept: %v", p.Source)
	}
	if p.Status != domainplace.StatusUnknown {
		t.Fatalf("default status = %s, want unknown", p.Status)
	}
}

func TestCreateValidates(t *testing.T) {
	svc := appplace.NewService(&fakePlaces{}, fakeClock{now})
	f := domainplace.Fields{Category: ptr(domainplace.CategoryShelter), Latitude: ptr(13.75), Longitude: ptr(100.5)}
	if _, err := svc.Create(context.Background(), f); !hasCode(err, apperr.CodeValidation) {
		t.Fatalf("missing name: want validation error, got %v", err)
	}
}

func TestOperatorCanChangeLegacyCommunityPlace(t *testing.T) {
	legacy := domainplace.Place{ID: uuid.New(), Name: "Boat", Status: domainplace.StatusOpen, CreatedByDevice: ptr("device-aaaaaaaa")}
	repo := &fakePlaces{places: []domainplace.Place{legacy}}
	svc := appplace.NewService(repo, fakeClock{now})
	ctx := context.Background()
	p, err := svc.Update(ctx, legacy.ID, domainplace.Fields{Status: ptr(domainplace.StatusClosed)})
	if err != nil || p.Status != domainplace.StatusClosed {
		t.Fatalf("update: %v %+v", err, p)
	}
	if err := svc.Delete(ctx, legacy.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(repo.places) != 0 {
		t.Fatalf("want 0 places left, got %d", len(repo.places))
	}
}

func TestListRequiresBBox(t *testing.T) {
	svc := appplace.NewService(&fakePlaces{}, fakeClock{now})
	if _, err := svc.List(context.Background(), appplace.ListInput{}); !hasCode(err, apperr.CodeValidation) {
		t.Fatalf("want validation error, got %v", err)
	}
}
