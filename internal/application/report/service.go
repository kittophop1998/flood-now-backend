// Package report contains the FloodNow report use cases: orchestration of
// domain rules + the ReportRepository port. No Gin, no SQL here.
package report

import (
	"context"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

type Service struct {
	repo  ports.ReportRepository
	clock ports.Clock
	ttl   time.Duration
}

func NewService(repo ports.ReportRepository, clock ports.Clock, ttl time.Duration) *Service {
	return &Service{repo: repo, clock: clock, ttl: ttl}
}

func (s *Service) Create(ctx context.Context, in domainreport.NewReportInput) (*domainreport.ReportWithStats, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	now := s.clock.Now()
	r := &domainreport.Report{
		ID:             uuid.New(),
		Type:           in.Type,
		Severity:       in.Severity,
		Latitude:       in.Latitude,
		Longitude:      in.Longitude,
		WaterLevelCM:   in.WaterLevelCM,
		Description:    in.Description,
		ImageKey:       in.ImageKey,
		PeopleCount:    in.PeopleCount,
		HasChild:       in.HasChild,
		HasElderly:     in.HasElderly,
		ContactPhone:   in.ContactPhone,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastVerifiedAt: now,
		ExpiresAt:      now.Add(s.ttl),
	}

	if err := s.repo.Create(ctx, r); err != nil {
		return nil, err
	}

	return &domainreport.ReportWithStats{Report: *r}, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*domainreport.ReportWithStats, error) {
	r, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("report not found")
	}
	return r, nil
}

type ListInput struct {
	BBox           *ports.BBox
	IncludeExpired bool
}

func (s *Service) List(ctx context.Context, in ListInput) ([]domainreport.ReportWithStats, error) {
	return s.repo.List(ctx, ports.ReportFilter{BBox: in.BBox, IncludeExpired: in.IncludeExpired})
}

// Confirm applies a still_active/cleared confirmation from a device. The
// business rule — still_active extends the report's freshness window,
// cleared does not — lives here, not in the repository.
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
	}
	if in.Status == domainreport.StatusStillActive {
		newExpiry := now.Add(s.ttl)
		params.NewExpiry = &newExpiry
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
