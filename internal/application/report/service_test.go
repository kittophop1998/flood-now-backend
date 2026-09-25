package report_test

import (
	"context"
	"errors"
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
// application layer's business rules without a real database. Confirm mirrors
// the Postgres adapter: upsert per device, refresh, then apply the policy.
type fakeRepo struct {
	reports    map[uuid.UUID]*domainreport.ReportWithStats
	votes      map[uuid.UUID]map[string]domainreport.ConfirmationStatus
	lastList   ports.ReportFilter
	lastNearby ports.NearbyFilter
	listResult []domainreport.ReportWithStats
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		reports: map[uuid.UUID]*domainreport.ReportWithStats{},
		votes:   map[uuid.UUID]map[string]domainreport.ConfirmationStatus{},
	}
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
	f.lastList = filter
	return f.listResult, nil
}

func (f *fakeRepo) Nearby(ctx context.Context, filter ports.NearbyFilter) ([]domainreport.ReportWithStats, error) {
	f.lastNearby = filter
	return nil, nil
}

func (f *fakeRepo) Confirm(ctx context.Context, p ports.ConfirmParams) (*domainreport.ReportWithStats, error) {
	r, ok := f.reports[p.ReportID]
	if !ok {
		return nil, nil
	}
	if f.votes[p.ReportID] == nil {
		f.votes[p.ReportID] = map[string]domainreport.ConfirmationStatus{}
	}
	f.votes[p.ReportID][p.DeviceID] = p.Status
	if p.Refresh != nil {
		r.LastVerifiedAt = p.Now
		r.StaleAt = p.Refresh.StaleAt
		r.ExpiresAt = p.Refresh.ExpiresAt
	}
	r.StillActiveCount, r.ClearedCount = 0, 0
	for _, s := range f.votes[p.ReportID] {
		if s == domainreport.StatusStillActive {
			r.StillActiveCount++
		} else {
			r.ClearedCount++
		}
	}
	r.ResolvedAt = p.Policy.NextResolvedAt(r.ResolvedAt, r.StillActiveCount, r.ClearedCount, p.Now)
	cp := *r
	return &cp, nil
}

var policy = domainreport.FreshnessPolicy{
	StaleAfter:         2 * time.Hour,
	TTL:                6 * time.Hour,
	FacilityStaleAfter: 12 * time.Hour,
	FacilityTTL:        48 * time.Hour,
	ResolveThreshold:   2,
}

func newTestService(now time.Time) (*appreport.Service, *fakeRepo, *fakeClock) {
	repo := newFakeRepo()
	clock := &fakeClock{now: now}
	return appreport.NewService(repo, clock, policy), repo, clock
}

func validInput() domainreport.NewReportInput {
	return domainreport.NewReportInput{
		Type:      domainreport.TypeFlooded,
		Severity:  domainreport.SeverityCritical,
		Latitude:  13.75,
		Longitude: 100.5,
	}
}

const deviceA = "12345678-aaaa-bbbb-cccc-dddddddddddd"
const deviceB = "87654321-aaaa-bbbb-cccc-dddddddddddd"
const deviceC = "abcdefab-aaaa-bbbb-cccc-dddddddddddd"

var t0 = time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)

func TestServiceCreateSetsLifecycleWindowByCategory(t *testing.T) {
	svc, _, _ := newTestService(t0)

	flood, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !flood.StaleAt.Equal(t0.Add(2*time.Hour)) || !flood.ExpiresAt.Equal(t0.Add(6*time.Hour)) {
		t.Errorf("flood stale/expiry = %v / %v", flood.StaleAt, flood.ExpiresAt)
	}
	if !flood.LastVerifiedAt.Equal(t0) || flood.Status(t0) != domainreport.StatusActive {
		t.Errorf("new report should be active and verified now")
	}

	in := validInput()
	in.Type = domainreport.TypeShelter
	shelter, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !shelter.ExpiresAt.Equal(t0.Add(48 * time.Hour)) {
		t.Errorf("shelter expiry = %v, want +48h", shelter.ExpiresAt)
	}
}

func TestServiceCreateNormalizesIrrelevantFields(t *testing.T) {
	svc, _, _ := newTestService(t0)
	in := validInput()
	in.Type = domainreport.TypeHelpNeeded
	d := domainreport.WaterDepthKnee
	in.WaterDepth = &d
	in.Passability = &domainreport.Passability{Walk: "passable", Motorcycle: "passable", Sedan: "passable", SUVPickup: "passable"}

	r, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.WaterDepth != nil || r.Passability != nil {
		t.Error("help_needed report should not keep water depth / passability")
	}
	if r.GeometryType != domainreport.GeometryPoint {
		t.Errorf("geometry_type = %q, want point", r.GeometryType)
	}
}

func TestServiceCreateRejectsInvalidInput(t *testing.T) {
	svc, _, _ := newTestService(t0)

	in := validInput()
	in.Type = "bogus"
	_, err := svc.Create(context.Background(), in)
	assertCode(t, err, apperr.CodeValidation)
}

func TestServiceGetNotFound(t *testing.T) {
	svc, _, _ := newTestService(t0)
	_, err := svc.Get(context.Background(), uuid.New())
	assertCode(t, err, apperr.CodeNotFound)
}

func TestServiceConfirmStillActiveRestartsFreshness(t *testing.T) {
	svc, _, clock := newTestService(t0)
	created, _ := svc.Create(context.Background(), validInput())

	clock.now = t0.Add(3 * time.Hour) // past stale_at: report reads possibly_stale
	if created.Status(clock.now) != domainreport.StatusPossiblyStale {
		t.Fatalf("precondition: expected possibly_stale")
	}

	confirmed, err := svc.Confirm(context.Background(), created.ID, domainreport.NewConfirmationInput{DeviceID: deviceA, Status: domainreport.StatusStillActive})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmed.StaleAt.Equal(clock.now.Add(2*time.Hour)) || !confirmed.ExpiresAt.Equal(clock.now.Add(6*time.Hour)) {
		t.Errorf("stale/expiry not restarted: %v / %v", confirmed.StaleAt, confirmed.ExpiresAt)
	}
	if confirmed.Status(clock.now) != domainreport.StatusActive || confirmed.StillActiveCount != 1 {
		t.Errorf("expected active with 1 confirmation, got %s / %d", confirmed.Status(clock.now), confirmed.StillActiveCount)
	}
}

func TestServiceConfirmClearedResolvesOnlyAtThreshold(t *testing.T) {
	svc, _, _ := newTestService(t0)
	created, _ := svc.Create(context.Background(), validInput())
	originalExpiry := created.ExpiresAt

	one, err := svc.Confirm(context.Background(), created.ID, domainreport.NewConfirmationInput{DeviceID: deviceA, Status: domainreport.StatusCleared})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if one.ResolvedAt != nil {
		t.Error("one cleared vote must not resolve the report")
	}
	if !one.ExpiresAt.Equal(originalExpiry) {
		t.Error("cleared vote must not change expiry")
	}

	// Same device again: upsert, still one vote.
	again, _ := svc.Confirm(context.Background(), created.ID, domainreport.NewConfirmationInput{DeviceID: deviceA, Status: domainreport.StatusCleared})
	if again.ClearedCount != 1 || again.ResolvedAt != nil {
		t.Errorf("repeat vote from one device must not count twice (cleared=%d)", again.ClearedCount)
	}

	two, _ := svc.Confirm(context.Background(), created.ID, domainreport.NewConfirmationInput{DeviceID: deviceB, Status: domainreport.StatusCleared})
	if two.ResolvedAt == nil || two.Status(t0) != domainreport.StatusResolved {
		t.Fatalf("two cleared votes should resolve the report, got %s", two.Status(t0))
	}

	// Two people say it's still happening → re-opened.
	svc.Confirm(context.Background(), created.ID, domainreport.NewConfirmationInput{DeviceID: deviceC, Status: domainreport.StatusStillActive}) //nolint:errcheck
	reopened, _ := svc.Confirm(context.Background(), created.ID, domainreport.NewConfirmationInput{DeviceID: deviceB, Status: domainreport.StatusStillActive})
	if reopened.ResolvedAt != nil {
		t.Error("report should re-open once still-active votes outnumber cleared")
	}
}

func TestServiceConfirmUnknownReportNotFound(t *testing.T) {
	svc, _, _ := newTestService(t0)
	for _, status := range []domainreport.ConfirmationStatus{domainreport.StatusStillActive, domainreport.StatusCleared} {
		_, err := svc.Confirm(context.Background(), uuid.New(), domainreport.NewConfirmationInput{DeviceID: deviceA, Status: status})
		assertCode(t, err, apperr.CodeNotFound)
	}
}

func TestServiceListDefaultsAndTruncation(t *testing.T) {
	svc, repo, _ := newTestService(t0)
	repo.listResult = make([]domainreport.ReportWithStats, 3)

	res, err := svc.List(context.Background(), appreport.ListInput{Limit: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.HasMore || len(res.Reports) != 2 {
		t.Errorf("expected 2 reports with has_more, got %d / %v", len(res.Reports), res.HasMore)
	}
	if repo.lastList.Limit != 3 {
		t.Errorf("repo limit = %d, want limit+1", repo.lastList.Limit)
	}
	got := repo.lastList.Statuses
	if len(got) != 2 || got[0] != domainreport.StatusActive || got[1] != domainreport.StatusPossiblyStale {
		t.Errorf("default statuses = %v, want active+possibly_stale (no resolved/expired)", got)
	}

	svc.List(context.Background(), appreport.ListInput{Limit: 99999}) //nolint:errcheck
	if repo.lastList.Limit != appreport.MaxListLimit+1 {
		t.Errorf("limit not clamped: %d", repo.lastList.Limit)
	}
}

func TestServiceFindDuplicatesUsesCategoryRadius(t *testing.T) {
	svc, repo, _ := newTestService(t0)

	if _, err := svc.FindDuplicates(context.Background(), 13.7, 100.5, domainreport.TypeObstruction); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f := repo.lastNearby
	if f.RadiusM != domainreport.TypeObstruction.DuplicateRadiusMeters() {
		t.Errorf("radius = %v", f.RadiusM)
	}
	if len(f.Types) != 1 || f.Types[0] != domainreport.TypeObstruction {
		t.Errorf("types = %v, want only the same category", f.Types)
	}
	if f.Sort != ports.SortDistance {
		t.Errorf("sort = %v, want distance", f.Sort)
	}

	_, err := svc.FindDuplicates(context.Background(), 13.7, 100.5, "bogus")
	assertCode(t, err, apperr.CodeValidation)
}

func TestServiceNearbyValidatesAndDefaults(t *testing.T) {
	svc, repo, _ := newTestService(t0)

	if _, err := svc.Nearby(context.Background(), appreport.NearbyInput{Latitude: 13.7, Longitude: 100.5}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastNearby.RadiusM != appreport.DefaultNearbyRadiusM || repo.lastNearby.Sort != ports.SortDistance {
		t.Errorf("defaults not applied: %+v", repo.lastNearby)
	}

	_, err := svc.Nearby(context.Background(), appreport.NearbyInput{Latitude: 13.7, Longitude: 100.5, RadiusM: 100000})
	assertCode(t, err, apperr.CodeValidation)
	_, err = svc.Nearby(context.Background(), appreport.NearbyInput{Latitude: 13.7, Longitude: 100.5, Sort: "random"})
	assertCode(t, err, apperr.CodeValidation)
}

func assertCode(t *testing.T, err error, code apperr.Code) {
	t.Helper()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
