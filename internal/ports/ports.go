// Package ports defines the boundaries the application layer depends on.
// Adapters implement these; domain/application never import an adapter.
package ports

import (
	"context"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/announcement"
	"floodnow-api/internal/domain/follow"
	"floodnow-api/internal/domain/importantplace"
	"floodnow-api/internal/domain/moderation"
	"floodnow-api/internal/domain/officialflood"
	"floodnow-api/internal/domain/place"
	"floodnow-api/internal/domain/report"
	"floodnow-api/internal/domain/route"
	"floodnow-api/internal/domain/sos"
)

// Clock is the only indirection over time.Now(), kept because expiry
// behavior needs to be deterministically testable.
type Clock interface {
	Now() time.Time
}

// ReportFilter narrows GET /reports to a map viewport and/or attributes.
// Empty slices mean "no filter" for that attribute. Hidden (moderated)
// reports are never returned.
type ReportFilter struct {
	BBox *BBox
	// BBoxes, when set, matches reports inside any of the boxes (a route
	// corridor). Combined with BBox by AND if both are set.
	BBoxes       []BBox
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
// apply Update's non-nil fields to the report (already normalized for its
// category; recorded as an "updated" event instead of "confirmed"), then
// re-apply Policy's resolution rule to the new vote counts and record the
// resulting events.
type ConfirmParams struct {
	ReportID uuid.UUID
	DeviceID string
	Status   report.ConfirmationStatus
	Now      time.Time
	Refresh  *Freshness
	Update   *report.ConditionUpdate
	Policy   report.FreshnessPolicy
}

type Freshness struct {
	StaleAt   time.Time
	ExpiresAt time.Time
}

// AggregateFilter selects open, visible reports inside BBox and groups them
// into square grid cells of CellDeg degrees.
type AggregateFilter struct {
	BBox       BBox
	CellDeg    float64
	Types      []report.Type
	Severities []report.Severity
	Statuses   []report.Status
	Now        time.Time
	MaxCells   int
}

// AggregateCell is one grid cell with at least one report.
type AggregateCell struct {
	Latitude, Longitude float64 // mean position of the cell's reports
	Count               int
	SevereCount         int
	MaxSeverity         report.Severity
	LatestUpdateAt      time.Time
}

// ReportEvent is one entry of a report's history (admin view).
type ReportEvent struct {
	ID        int64
	Kind      report.EventKind
	CreatedAt time.Time
}

type ReportRepository interface {
	// Create persists r and records its "created" event. It returns a
	// CONFLICT *apperr.Error if r.ClientID was already used.
	Create(ctx context.Context, r *report.Report) error
	// GetByID returns the report even when hidden; callers decide visibility.
	GetByID(ctx context.Context, id uuid.UUID) (*report.ReportWithStats, error)
	FindByClientID(ctx context.Context, clientID string) (*report.ReportWithStats, error)
	List(ctx context.Context, filter ReportFilter) ([]report.ReportWithStats, error)
	Nearby(ctx context.Context, filter NearbyFilter) ([]report.ReportWithStats, error)
	Aggregate(ctx context.Context, filter AggregateFilter) ([]AggregateCell, error)
	Confirm(ctx context.Context, params ConfirmParams) (*report.ReportWithStats, error)
	Events(ctx context.Context, reportID uuid.UUID) ([]ReportEvent, error)
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
	// ListByDevice / CountByDevice only consider the given kinds.
	ListByDevice(ctx context.Context, deviceID string, kinds ...follow.Kind) ([]follow.Follow, error)
	CountByDevice(ctx context.Context, deviceID string, kinds ...follow.Kind) (int, error)
	// FindReportFollow returns the device's existing follow of reportID, or nil.
	FindReportFollow(ctx context.Context, deviceID string, reportID uuid.UUID) (*follow.Follow, error)
	Create(ctx context.Context, f *follow.Follow) error
	// Delete removes the follow only if it belongs to deviceID and is one of
	// kinds; false if none matched.
	Delete(ctx context.Context, deviceID string, id uuid.UUID, kinds ...follow.Kind) (bool, error)
	Notifications(ctx context.Context, q NotificationQuery) ([]NotificationCandidate, error)

	// Saved places (follows of kind "place").
	GetPlace(ctx context.Context, deviceID string, id uuid.UUID) (*follow.Follow, error)
	UpdatePlace(ctx context.Context, f *follow.Follow) error
	// PlaceSummaries returns the device's saved places with the open, visible
	// reports inside each watch radius counted in a single query.
	PlaceSummaries(ctx context.Context, deviceID string, severe []report.Severity, now time.Time) ([]follow.PlaceWithSummary, error)
}

// RouteProvider asks an external routing engine for candidate routes. The
// application never computes routes itself.
type RouteProvider interface {
	// Routes returns up to a few alternatives, best first; an empty slice
	// means the provider found no route. Provider failures are UNAVAILABLE.
	Routes(ctx context.Context, origin, destination route.Point, profile route.Profile) ([]route.Candidate, error)
}

// NearbySOSQuery selects waiting requests a helper could answer.
type NearbySOSQuery struct {
	Latitude, Longitude float64
	RadiusM             float64
	Types               []sos.Type
	ExcludeDeviceID     string
	CreatedSince        time.Time
	Limit               int
}

// SOSWithDistance is a request plus its distance from the query point.
type SOSWithDistance struct {
	sos.Request
	DistanceM float64
}

// HelperCountQuery counts active helpers able to answer a request.
type HelperCountQuery struct {
	Latitude, Longitude float64
	Capabilities        []sos.Capability
	ExcludeDeviceID     string
	LocationSince       time.Time
}

// SOSTransition is an atomic status change: it applies only if the request
// is still in From (and, for helper actions, still assigned to HelperDeviceID).
type SOSTransition struct {
	ID             uuid.UUID
	From, To       sos.Status
	Actor          sos.Role
	HelperDeviceID *string // required when Actor is helper
	Now            time.Time
}

type SOSRepository interface {
	// Create inserts the request and its first timeline event. It returns a
	// CONFLICT *apperr.Error if the device already has an open request.
	Create(ctx context.Context, r *sos.Request) error
	Get(ctx context.Context, id uuid.UUID) (*sos.Request, error)
	FindByClientID(ctx context.Context, deviceID, clientID string) (*sos.Request, error)
	Events(ctx context.Context, id uuid.UUID) ([]sos.Event, error)
	// ListForDevice returns requests the device made or is helping with,
	// open ones first.
	ListForDevice(ctx context.Context, deviceID string, limit int) ([]sos.Request, error)
	// Transition applies t atomically; false if the request was not in the
	// expected state (someone else changed it first).
	Transition(ctx context.Context, t SOSTransition) (bool, error)
	// Accept assigns a waiting request to helperDeviceID in one transaction,
	// refusing if it is no longer waiting or the helper already holds
	// maxActive open assignments. ok is false when it was already taken.
	Accept(ctx context.Context, id uuid.UUID, helperDeviceID string, maxActive int, now time.Time) (ok bool, err error)
	NearbyWaiting(ctx context.Context, q NearbySOSQuery) ([]SOSWithDistance, error)
	CountHelpersNear(ctx context.Context, q HelperCountQuery) (int, error)

	GetHelper(ctx context.Context, deviceID string) (*sos.Helper, error)
	UpsertHelper(ctx context.Context, h *sos.Helper) error
	UpdateHelperLocation(ctx context.Context, deviceID string, lat, lng float64, at time.Time) error
}

// ImportantPlaceFilter narrows the important-places layer to a viewport.
type ImportantPlaceFilter struct {
	BBox       *BBox
	Categories []importantplace.Category
	Statuses   []importantplace.Status
	Limit      int
}

type ImportantPlaceRepository interface {
	List(ctx context.Context, filter ImportantPlaceFilter) ([]importantplace.Place, error)
	Get(ctx context.Context, id uuid.UUID) (*importantplace.Place, error)
	Create(ctx context.Context, p *importantplace.Place) error
	Update(ctx context.Context, p *importantplace.Place) error
	Delete(ctx context.Context, id uuid.UUID) (bool, error)
	// CountByDeviceSince counts places the device added at or after since.
	CountByDeviceSince(ctx context.Context, deviceID string, since time.Time) (int, error)
}

// AnnouncementFilter selects announcements. Published-only unless
// IncludeDrafts; live-only (started, not ended) unless IncludeExpired, which
// adds those that ended after ExpiredSince.
type AnnouncementFilter struct {
	Now            time.Time
	IncludeDrafts  bool
	IncludeExpired bool
	ExpiredSince   time.Time
	// BBox keeps announcements whose point/area intersects it, plus those
	// with no location (they apply everywhere).
	BBox  *BBox
	Limit int
}

type AnnouncementRepository interface {
	List(ctx context.Context, filter AnnouncementFilter) ([]announcement.Announcement, error)
	Get(ctx context.Context, id uuid.UUID) (*announcement.Announcement, error)
	Create(ctx context.Context, a *announcement.Announcement) error
	Update(ctx context.Context, a *announcement.Announcement) error
	Delete(ctx context.Context, id uuid.UUID) (bool, error)
}

// ComplaintParams is the atomic write for a new complaint: insert it (a
// device's repeat complaint about the same report is ignored), then apply
// Policy's auto-hide rule to the pending count.
type ComplaintParams struct {
	Complaint moderation.Complaint
	Policy    moderation.Policy
	Now       time.Time
}

// ModerationQueueItem is one reported incident awaiting review.
type ModerationQueueItem struct {
	Report         report.ReportWithStats
	ComplaintCount int
	Reasons        map[moderation.Reason]int
	LatestAt       time.Time
	Complaints     []moderation.Complaint // newest first, capped
	Events         []ReportEvent
}

type ModerationRepository interface {
	// AddComplaint returns the stored complaint (the existing one on a
	// repeat) and whether this call newly hid the report. nil if the report
	// doesn't exist.
	AddComplaint(ctx context.Context, p ComplaintParams) (c *moderation.Complaint, hidden bool, err error)
	CountByDeviceSince(ctx context.Context, deviceID string, since time.Time) (int, error)
	Queue(ctx context.Context, limit int) ([]ModerationQueueItem, error)
	// Apply performs an operator action: set/clear the report's hidden state
	// (recording hidden/unhidden events) and close its pending complaints.
	// false if the report doesn't exist.
	Apply(ctx context.Context, reportID uuid.UUID, action moderation.Action, now time.Time) (bool, error)
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

// FloodAreaProvider fetches one period's official flood areas (GISTDA) in
// full. Callers cache the result; the provider is never called per map move.
// Failures are UNAVAILABLE.
type FloodAreaProvider interface {
	FloodAreas(ctx context.Context, period officialflood.Period) (*officialflood.Snapshot, error)
}
