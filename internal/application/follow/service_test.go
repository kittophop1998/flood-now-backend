package follow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	appfollow "floodnow-api/internal/application/follow"
	"floodnow-api/internal/domain/apperr"
	domainfollow "floodnow-api/internal/domain/follow"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

type fakeFollows struct {
	follows    []domainfollow.Follow
	candidates []ports.NotificationCandidate
	lastQuery  ports.NotificationQuery
}

func (f *fakeFollows) ListByDevice(ctx context.Context, deviceID string) ([]domainfollow.Follow, error) {
	return f.follows, nil
}

func (f *fakeFollows) CountByDevice(ctx context.Context, deviceID string) (int, error) {
	return len(f.follows), nil
}

func (f *fakeFollows) FindReportFollow(ctx context.Context, deviceID string, reportID uuid.UUID) (*domainfollow.Follow, error) {
	for _, fl := range f.follows {
		if fl.ReportID != nil && *fl.ReportID == reportID {
			cp := fl
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeFollows) Create(ctx context.Context, fl *domainfollow.Follow) error {
	f.follows = append(f.follows, *fl)
	return nil
}

func (f *fakeFollows) Delete(ctx context.Context, deviceID string, id uuid.UUID) (bool, error) {
	return false, nil
}

func (f *fakeFollows) Notifications(ctx context.Context, q ports.NotificationQuery) ([]ports.NotificationCandidate, error) {
	f.lastQuery = q
	return f.candidates, nil
}

// fakeReports implements only GetByID; the follow service needs nothing else.
type fakeReports struct {
	ports.ReportRepository
	existing map[uuid.UUID]bool
}

func (f fakeReports) GetByID(ctx context.Context, id uuid.UUID) (*domainreport.ReportWithStats, error) {
	if !f.existing[id] {
		return nil, nil
	}
	return &domainreport.ReportWithStats{Report: domainreport.Report{ID: id}}, nil
}

const device = "12345678-aaaa-bbbb-cccc-dddddddddddd"

var now = time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func TestCreateReportFollowIsIdempotent(t *testing.T) {
	reportID := uuid.New()
	follows := &fakeFollows{}
	svc := appfollow.NewService(follows, fakeReports{existing: map[uuid.UUID]bool{reportID: true}}, fakeClock{now})

	first, err := svc.Create(context.Background(), domainfollow.NewFollowInput{DeviceID: device, Kind: domainfollow.KindReport, ReportID: &reportID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := svc.Create(context.Background(), domainfollow.NewFollowInput{DeviceID: device, Kind: domainfollow.KindReport, ReportID: &reportID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.ID != second.ID || len(follows.follows) != 1 {
		t.Errorf("following the same report twice created %d follows", len(follows.follows))
	}
}

func TestCreateReportFollowUnknownReport(t *testing.T) {
	svc := appfollow.NewService(&fakeFollows{}, fakeReports{}, fakeClock{now})
	_, err := svc.Create(context.Background(), domainfollow.NewFollowInput{DeviceID: device, Kind: domainfollow.KindReport, ReportID: ptr(uuid.New())})
	assertCode(t, err, apperr.CodeNotFound)
}

func TestCreateFollowEnforcesPerDeviceCap(t *testing.T) {
	follows := &fakeFollows{follows: make([]domainfollow.Follow, domainfollow.MaxPerDevice)}
	svc := appfollow.NewService(follows, fakeReports{}, fakeClock{now})
	_, err := svc.Create(context.Background(), domainfollow.NewFollowInput{
		DeviceID: device, Kind: domainfollow.KindArea, Latitude: ptr(13.7), Longitude: ptr(100.5), RadiusM: ptr(1000),
	})
	assertCode(t, err, apperr.CodeConflict)
}

func TestNotificationsMapsFiltersAndDedupes(t *testing.T) {
	areaFollow, reportFollow := uuid.New(), uuid.New()
	severe := domainreport.ReportWithStats{Report: domainreport.Report{ID: uuid.New(), Severity: domainreport.SeverityCritical}}
	mild := domainreport.ReportWithStats{Report: domainreport.Report{ID: uuid.New(), Severity: domainreport.SeverityLow}}

	follows := &fakeFollows{candidates: []ports.NotificationCandidate{
		{EventID: 3, EventKind: domainreport.EventResolved, FollowID: reportFollow, FollowKind: domainfollow.KindReport, Report: mild},
		{EventID: 2, EventKind: domainreport.EventCreated, FollowID: areaFollow, FollowKind: domainfollow.KindArea, Report: severe},
		{EventID: 2, EventKind: domainreport.EventCreated, FollowID: uuid.New(), FollowKind: domainfollow.KindArea, Report: severe}, // same event, 2nd area
		{EventID: 1, EventKind: domainreport.EventCreated, FollowID: areaFollow, FollowKind: domainfollow.KindArea, Report: mild},
	}}
	svc := appfollow.NewService(follows, fakeReports{}, fakeClock{now})

	items, err := svc.Notifications(context.Background(), device, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d notifications, want 2: %+v", len(items), items)
	}
	if items[0].Kind != domainfollow.NotifyResolved || items[1].Kind != domainfollow.NotifySevereNearby {
		t.Errorf("unexpected kinds: %s, %s", items[0].Kind, items[1].Kind)
	}
	if !follows.lastQuery.Since.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Errorf("missing since should fall back to the max lookback, got %v", follows.lastQuery.Since)
	}

	recent := now.Add(-time.Hour)
	svc.Notifications(context.Background(), device, &recent) //nolint:errcheck
	if !follows.lastQuery.Since.Equal(recent) {
		t.Errorf("since not honored: %v", follows.lastQuery.Since)
	}
}

func assertCode(t *testing.T, err error, code apperr.Code) {
	t.Helper()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
