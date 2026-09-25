// Package ports defines the boundaries the application layer depends on.
// Adapters implement these; domain/application never import an adapter.
package ports

import (
	"context"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/follow"
	"floodnow-api/internal/domain/place"
	"floodnow-api/internal/domain/report"
)

// Clock is the only indirection over time.Now(), kept because expiry
// behavior needs to be deterministically testable.
type Clock interface {
	Now() time.Time
}

// ReportFilter narrows GET /reports to a map viewport and/or attributes.
// Empty slices mean "no filter" for that attribute.
type ReportFilter struct {
	BBox         *BBox
	Types        []report.Type
	Severities   []report.Severity
	Statuses     []report.Status
	UpdatedSince *time.Time
	Limit        int
	// Now is the instant statuses are evaluated at (the app clock, so API
	// responses and filtering agree).
	Now time.Time
}

type BBox struct {
	MinLat, MaxLat, MinLng, MaxLng float64
}

type NearbySort string

const (
	SortDistance NearbySort = "distance"
	SortRecent   NearbySort = "recent"
	SortSeverity NearbySort = "severity"
)

// NearbyFilter selects reports within RadiusM of a point. Results carry
// DistanceM.
type NearbyFilter struct {
	Latitude, Longitude float64
	RadiusM             float64
	Types               []report.Type
	Statuses            []report.Status
	Sort                NearbySort
	Limit               int
	Now                 time.Time
}

// ConfirmParams is the atomic write the repository performs for a
// confirmation: upsert the (report_id, device_id) row, extend the parent
// report's freshness when Refresh is set (decided by the application layer),
// then re-apply Policy's resolution rule to the new vote counts and record
// the resulting events.
type ConfirmParams struct {
	ReportID uuid.UUID
	DeviceID string
	Status   report.ConfirmationStatus
	Now      time.Time
	Refresh  *Freshness
	Policy   report.FreshnessPolicy
}

type Freshness struct {
	StaleAt   time.Time
	ExpiresAt time.Time
}

type ReportRepository interface {
	// Create persists r and records its "created" event.
	Create(ctx context.Context, r *report.Report) error
	GetByID(ctx context.Context, id uuid.UUID) (*report.ReportWithStats, error)
	List(ctx context.Context, filter ReportFilter) ([]report.ReportWithStats, error)
	Nearby(ctx context.Context, filter NearbyFilter) ([]report.ReportWithStats, error)
	Confirm(ctx context.Context, params ConfirmParams) (*report.ReportWithStats, error)
}

// NotificationQuery selects report events relevant to a device's follows.
type NotificationQuery struct {
	DeviceID string
	Since    time.Time
	Limit    int
	// AreaSeverities limits area-follow matches to new reports this severe.
	AreaSeverities []report.Severity
}

// NotificationCandidate is one (event, follow) match; the application layer
// decides what, if anything, it means to the user.
type NotificationCandidate struct {
	EventID    int64
	EventKind  report.EventKind
	CreatedAt  time.Time
	FollowID   uuid.UUID
	FollowKind follow.Kind
	Report     report.ReportWithStats
}

type FollowRepository interface {
	ListByDevice(ctx context.Context, deviceID string) ([]follow.Follow, error)
	CountByDevice(ctx context.Context, deviceID string) (int, error)
	// FindReportFollow returns the device's existing follow of reportID, or nil.
	FindReportFollow(ctx context.Context, deviceID string, reportID uuid.UUID) (*follow.Follow, error)
	Create(ctx context.Context, f *follow.Follow) error
	// Delete removes the follow only if it belongs to deviceID; false if none matched.
	Delete(ctx context.Context, deviceID string, id uuid.UUID) (bool, error)
	Notifications(ctx context.Context, q NotificationQuery) ([]NotificationCandidate, error)
}

// Presigner generates a time-limited direct-upload URL for object storage
// (Cloudflare R2). Implemented by the storage adapter.
type Presigner interface {
	PresignUpload(ctx context.Context, objectKey, contentType string, contentLength int64) (uploadURL string, expiresIn time.Duration, err error)
}

// Geocoder resolves place names ⇄ coordinates via an external provider.
type Geocoder interface {
	Search(ctx context.Context, query, lang string, near *BBox, limit int) ([]place.Place, error)
	// Reverse returns nil when nothing is known at that point.
	Reverse(ctx context.Context, lat, lng float64, lang string) (*place.Place, error)
}
