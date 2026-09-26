// Package report contains the FloodNow report use cases: orchestration of
// domain rules + the ReportRepository port. No Gin, no SQL here.
package report

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

const (
	// DefaultListLimit / MaxListLimit bound a viewport query so a zoomed-out
	// map never pulls every report at once; the response says when it was cut.
	DefaultListLimit = 500
	MaxListLimit     = 1000

	DefaultNearbyRadiusM = 5000.0
	MaxNearbyRadiusM     = 50000.0
	DefaultNearbyLimit   = 50
	MaxNearbyLimit       = 100

	maxDuplicateCandidates = 3

	// Aggregation: at zoom z a cell is 1/8 of a 512 px map tile (~64 px), so
	// zones stay readable at every zoom and the cell count stays bounded by
	// the viewport size.
	MinAggregateZoom  = 0
	MaxAggregateZoom  = 16
	maxAggregateCells = 2000
)

type Service struct {
	repo   ports.ReportRepository
	clock  ports.Clock
	policy domainreport.FreshnessPolicy
}

func NewService(repo ports.ReportRepository, clock ports.Clock, policy domainreport.FreshnessPolicy) *Service {
	return &Service{repo: repo, clock: clock, policy: policy}
}

// Create stores a new report. With a ClientID (offline queue) the call is
// idempotent: re-sending returns the report created the first time.
func (s *Service) Create(ctx context.Context, in domainreport.NewReportInput) (*domainreport.ReportWithStats, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	in = in.Normalized()
	if in.ClientID != nil {
		existing, err := s.repo.FindByClientID(ctx, *in.ClientID)
		if err != nil || existing != nil {
			return existing, err
		}
	}

	now := s.clock.Now()
	staleAt, expiresAt := s.policy.Window(in.Type, now)
	r := &domainreport.Report{
		ID:             uuid.New(),
		Type:           in.Type,
		Severity:       in.Severity,
		Latitude:       in.Latitude,
		Longitude:      in.Longitude,
		GeometryType:   in.GeometryType,
		WaterDepth:     in.WaterDepth,
		WaterLevelCM:   in.WaterLevelCM,
		Passability:    in.Passability,
		Description:    in.Description,
		ImageKey:       in.ImageKey,
		PeopleCount:    in.PeopleCount,
		HasChild:       in.HasChild,
		HasElderly:     in.HasElderly,
		ContactPhone:   in.ContactPhone,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastVerifiedAt: now,
		StaleAt:        staleAt,
		ExpiresAt:      expiresAt,
		ClientID:       in.ClientID,
	}

	if err := s.repo.Create(ctx, r); err != nil {
		var appErr *apperr.Error
		if in.ClientID != nil && errors.As(err, &appErr) && appErr.Code == apperr.CodeConflict {
			// A concurrent retry with the same client id won the insert.
			if existing, ferr := s.repo.FindByClientID(ctx, *in.ClientID); ferr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, err
	}

	return &domainreport.ReportWithStats{Report: *r}, nil
}

// Get returns a report by id. Reports hidden by moderation are not found.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*domainreport.ReportWithStats, error) {
	r, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil || r.HiddenAt != nil {
		return nil, apperr.NotFound("report not found")
	}
	return r, nil
}

type AggregateInput struct {
	BBox       *ports.BBox
	Zoom       int
	Types      []domainreport.Type
	Severities []domainreport.Severity
	Statuses   []domainreport.Status
}

type AggregateResult struct {
	Cells   []ports.AggregateCell
	CellDeg float64
	Total   int
}

// CellDegreesForZoom is the aggregation grid size at a map zoom level.
func CellDegreesForZoom(zoom int) float64 {
	return 360 / math.Pow(2, float64(zoom)) / 8
}

// Aggregate groups open reports in a viewport into grid cells for the
// zoomed-out map (heatmap / flood zones) instead of sending every report.
func (s *Service) Aggregate(ctx context.Context, in AggregateInput) (*AggregateResult, error) {
	fields := map[string]string{}
	if in.BBox == nil {
		fields["bbox"] = "min_lat, max_lat, min_lng, max_lng are required"
	}
	if in.Zoom < MinAggregateZoom || in.Zoom > MaxAggregateZoom {
		fields["zoom"] = "must be between 0 and 16"
	}
	if len(fields) > 0 {
		return nil, apperr.Validation("aggregate query is invalid", fields)
	}
	statuses := in.Statuses
	if len(statuses) == 0 {
		statuses = domainreport.DefaultVisibleStatuses
	}
	cellDeg := CellDegreesForZoom(in.Zoom)
	cells, err := s.repo.Aggregate(ctx, ports.AggregateFilter{
		BBox:       *in.BBox,
		CellDeg:    cellDeg,
		Types:      in.Types,
		Severities: in.Severities,
		Statuses:   statuses,
		Now:        s.clock.Now(),
		MaxCells:   maxAggregateCells,
	})
	if err != nil {
		return nil, err
	}
	res := &AggregateResult{Cells: cells, CellDeg: cellDeg}
	for _, c := range cells {
		res.Total += c.Count
	}
	return res, nil
}

type ListInput struct {
	BBox         *ports.BBox
	Types        []domainreport.Type
	Severities   []domainreport.Severity
	Statuses     []domainreport.Status
	UpdatedSince *time.Time
	Limit        int
}

type ListResult struct {
	Reports []domainreport.ReportWithStats
	// HasMore is true when more reports matched than Limit allowed; the map
	// asks the user to zoom in rather than silently dropping markers.
	HasMore bool
}

func (s *Service) List(ctx context.Context, in ListInput) (*ListResult, error) {
	limit := clampLimit(in.Limit, DefaultListLimit, MaxListLimit)
	statuses := in.Statuses
	if len(statuses) == 0 {
		statuses = domainreport.DefaultVisibleStatuses
	}
	filter := ports.ReportFilter{
		BBox:         in.BBox,
		Types:        in.Types,
		Severities:   in.Severities,
		Statuses:     statuses,
		UpdatedSince: in.UpdatedSince,
		Limit:        limit + 1, // one extra row tells us whether the result was truncated
		Now:          s.clock.Now(),
	}

	reports, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Reports: reports}
	if len(reports) > limit {
		res.Reports = reports[:limit]
		res.HasMore = true
	}
	return res, nil
}

type NearbyInput struct {
	Latitude, Longitude float64
	RadiusM             float64
	Types               []domainreport.Type
	Statuses            []domainreport.Status
	Sort                ports.NearbySort
	Limit               int
}

func (s *Service) Nearby(ctx context.Context, in NearbyInput) ([]domainreport.ReportWithStats, error) {
	fields := map[string]string{}
	if in.Latitude < -90 || in.Latitude > 90 {
		fields["lat"] = "must be between -90 and 90"
	}
	if in.Longitude < -180 || in.Longitude > 180 {
		fields["lng"] = "must be between -180 and 180"
	}
	if in.RadiusM < 0 || in.RadiusM > MaxNearbyRadiusM {
		fields["radius_m"] = "must be between 0 and 50000"
	}
	switch in.Sort {
	case "", ports.SortDistance, ports.SortRecent, ports.SortSeverity:
	default:
		fields["sort"] = "must be one of distance, recent, severity"
	}
	if len(fields) > 0 {
		return nil, apperr.Validation("nearby query is invalid", fields)
	}

	radius := in.RadiusM
	if radius == 0 {
		radius = DefaultNearbyRadiusM
	}
	sort := in.Sort
	if sort == "" {
		sort = ports.SortDistance
	}
	statuses := in.Statuses
	if len(statuses) == 0 {
		statuses = domainreport.DefaultVisibleStatuses
	}
	return s.repo.Nearby(ctx, ports.NearbyFilter{
		Latitude:  in.Latitude,
		Longitude: in.Longitude,
		RadiusM:   radius,
		Types:     in.Types,
		Statuses:  statuses,
		Sort:      sort,
		Limit:     clampLimit(in.Limit, DefaultNearbyLimit, MaxNearbyLimit),
		Now:       s.clock.Now(),
	})
}

// FindDuplicates returns open reports of the same category close enough to
// (lat, lng) that a new report there is probably the same incident. The
// radius comes from the category's duplicate rule. It never blocks creating
// a report — the client decides whether to confirm an existing one instead.
func (s *Service) FindDuplicates(ctx context.Context, lat, lng float64, t domainreport.Type) ([]domainreport.ReportWithStats, error) {
	fields := map[string]string{}
	if !t.Valid() {
		fields["type"] = "must be a valid report type"
	}
	if lat < -90 || lat > 90 {
		fields["lat"] = "must be between -90 and 90"
	}
	if lng < -180 || lng > 180 {
		fields["lng"] = "must be between -180 and 180"
	}
	if len(fields) > 0 {
		return nil, apperr.Validation("duplicate query is invalid", fields)
	}
	return s.repo.Nearby(ctx, ports.NearbyFilter{
		Latitude:  lat,
		Longitude: lng,
		RadiusM:   t.DuplicateRadiusMeters(),
		Types:     []domainreport.Type{t},
		Statuses:  domainreport.DefaultVisibleStatuses,
		Sort:      ports.SortDistance,
		Limit:     maxDuplicateCandidates,
		Now:       s.clock.Now(),
	})
}

// Confirm applies a still_active/cleared confirmation from a device. The
// business rules live here and in the domain policy, not in the repository:
// still_active restarts the report's freshness window and may carry a
// ConditionUpdate that replaces the report's current condition; either vote
// may resolve or re-open the report per FreshnessPolicy.NextResolvedAt.
func (s *Service) Confirm(ctx context.Context, reportID uuid.UUID, in domainreport.NewConfirmationInput) (*domainreport.ReportWithStats, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	now := s.clock.Now()
	params := ports.ConfirmParams{
		ReportID: reportID,
		DeviceID: in.DeviceID,
		Status:   in.Status,
		Now:      now,
		Policy:   s.policy,
	}
	existing, err := s.repo.GetByID(ctx, reportID)
	if err != nil {
		return nil, err
	}
	if existing == nil || existing.HiddenAt != nil {
		return nil, apperr.NotFound("report not found")
	}
	if in.Status == domainreport.StatusStillActive {
		staleAt, expiresAt := s.policy.Window(existing.Type, now)
		params.Refresh = &ports.Freshness{StaleAt: staleAt, ExpiresAt: expiresAt}
	}
	// Fields that don't apply to the category are dropped, not rejected
	// (same as create).
	if update := in.Update.For(existing.Type); !update.Empty() {
		params.Update = &update
	}

	r, err := s.repo.Confirm(ctx, params)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("report not found")
	}
	return r, nil
}

func clampLimit(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}
