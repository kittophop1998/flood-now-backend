package report_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	appreport "floodnow-api/internal/application/report"
	"floodnow-api/internal/domain/apperr"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// fakeRepo is a minimal in-memory ports.ReportRepository for exercising the
// application layer's business rules without a real database.
type fakeRepo struct {
	reports map[uuid.UUID]*domainreport.ReportWithStats
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{reports: map[uuid.UUID]*domainreport.ReportWithStats{}}
}

func (f *fakeRepo) Create(ctx context.Context, r *domainreport.Report) error {
	f.reports[r.ID] = &domainreport.ReportWithStats{Report: *r}
	return nil
}

func (f *fakeRepo) GetByID(ctx context.Context, id uuid.UUID) (*domainreport.ReportWithStats, error) {
	r, ok := f.reports[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (f *fakeRepo) List(ctx context.Context, filter ports.ReportFilter) ([]domainreport.ReportWithStats, error) {
	var out []domainreport.ReportWithStats
	for _, r := range f.reports {
		out = append(out, *r)
	}
	return out, nil
}

func (f *fakeRepo) Confirm(ctx context.Context, params ports.ConfirmParams) (*domainreport.ReportWithStats, error) {
	r, ok := f.reports[params.ReportID]
	if !ok {
		return nil, nil
	}
	if params.Status == domainreport.StatusStillActive {
		r.StillActiveCount++
	} else {
		r.ClearedCount++
	}
	if params.NewExpiry != nil {
		r.LastVerifiedAt = params.Now
		r.ExpiresAt = *params.NewExpiry
	}
	cp := *r
	return &cp, nil
}

func newTestService(now time.Time) (*appreport.Service, *fakeRepo) {
	repo := newFakeRepo()
	svc := appreport.NewService(repo, &fakeClock{now: now}, 2*time.Hour)
	return svc, repo
}

func validInput() domainreport.NewReportInput {
	return domainreport.NewReportInput{
		Type:      domainreport.TypeFlooded,
		Severity:  domainreport.SeverityImpassable,
		Latitude:  13.75,
		Longitude: 100.5,
	}
}

func TestServiceCreateSetsExpiryFromTTL(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	svc, _ := newTestService(now)

	r, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantExpiry := now.Add(2 * time.Hour)
	if !r.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("expires_at = %v, want %v", r.ExpiresAt, wantExpiry)
	}
	if !r.LastVerifiedAt.Equal(now) {
		t.Errorf("last_verified_at = %v, want %v", r.LastVerifiedAt, now)
	}
}

func TestServiceCreateRejectsInvalidInput(t *testing.T) {
	svc, _ := newTestService(time.Now())

	in := validInput()
	in.Type = "bogus"
	_, err := svc.Create(context.Background(), in)
	if err == nil {
		t.Fatal("expected validation error")
	}
	var appErr *apperr.Error
	if !asAppErr(err, &appErr) || appErr.Code != apperr.CodeValidation {
		t.Fatalf("expected VALIDATION_ERROR, got %v", err)
	}
}

func TestServiceGetNotFound(t *testing.T) {
	svc, _ := newTestService(time.Now())

	_, err := svc.Get(context.Background(), uuid.New())
	var appErr *apperr.Error
	if !asAppErr(err, &appErr) || appErr.Code != apperr.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

func TestServiceConfirmStillActiveExtendsExpiry(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	svc, _ := newTestService(now)

	created, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	later := now.Add(90 * time.Minute)
	svc2, repo := newTestService(later)
	repo.reports[created.ID] = created // reuse the same fake store with the later clock

	confirmed, err := svc2.Confirm(context.Background(), created.ID, domainreport.NewConfirmationInput{
		DeviceID: "12345678-aaaa-bbbb-cccc-dddddddddddd",
		Status:   domainreport.StatusStillActive,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantExpiry := later.Add(2 * time.Hour)
	if !confirmed.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("expires_at = %v, want %v", confirmed.ExpiresAt, wantExpiry)
	}
	if confirmed.StillActiveCount != 1 {
		t.Errorf("still_active_count = %d, want 1", confirmed.StillActiveCount)
	}
}

func TestServiceConfirmClearedDoesNotChangeExpiry(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	svc, repo := newTestService(now)

	created, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	originalExpiry := created.ExpiresAt

	confirmed, err := svc.Confirm(context.Background(), created.ID, domainreport.NewConfirmationInput{
		DeviceID: "12345678-aaaa-bbbb-cccc-dddddddddddd",
		Status:   domainreport.StatusCleared,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !confirmed.ExpiresAt.Equal(originalExpiry) {
		t.Errorf("cleared confirmation changed expires_at: got %v, want %v", confirmed.ExpiresAt, originalExpiry)
	}
	if confirmed.ClearedCount != 1 {
		t.Errorf("cleared_count = %d, want 1", confirmed.ClearedCount)
	}
	if _, ok := repo.reports[created.ID]; !ok {
		t.Error("cleared confirmation must not remove the report")
	}
}

func TestServiceConfirmUnknownReportNotFound(t *testing.T) {
	svc, _ := newTestService(time.Now())

	_, err := svc.Confirm(context.Background(), uuid.New(), domainreport.NewConfirmationInput{
		DeviceID: "12345678-aaaa-bbbb-cccc-dddddddddddd",
		Status:   domainreport.StatusStillActive,
	})
	var appErr *apperr.Error
	if !asAppErr(err, &appErr) || appErr.Code != apperr.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

func asAppErr(err error, target **apperr.Error) bool {
	ae, ok := err.(*apperr.Error)
	if !ok {
		return false
	}
	*target = ae
	return true
}
