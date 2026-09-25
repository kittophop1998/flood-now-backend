// Package moderation contains the "report a problem" and operator review use
// cases. Decisions are deterministic: a distinct-device threshold auto-hides
// a report pending review, and operators dismiss/hide/restore explicitly.
package moderation

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/moderation"
	"floodnow-api/internal/ports"
)

const queueLimit = 50

type Service struct {
	repo    ports.ModerationRepository
	reports ports.ReportRepository
	clock   ports.Clock
	policy  moderation.Policy
}

func NewService(repo ports.ModerationRepository, reports ports.ReportRepository, clock ports.Clock, policy moderation.Policy) *Service {
	return &Service{repo: repo, reports: reports, clock: clock, policy: policy}
}

// Report files a complaint about an incident. A device's second complaint
// about the same incident returns the first one; a device filing too many
// complaints in an hour is rate limited.
func (s *Service) Report(ctx context.Context, reportID uuid.UUID, in moderation.NewComplaintInput) (*moderation.Complaint, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	r, err := s.reports.GetByID(ctx, reportID)
	if err != nil {
		return nil, err
	}
	if r == nil || r.HiddenAt != nil {
		return nil, apperr.NotFound("report not found")
	}
	now := s.clock.Now()
	n, err := s.repo.CountByDeviceSince(ctx, in.DeviceID, now.Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	if n >= s.policy.MaxPerDevicePerHour {
		return nil, apperr.RateLimited("too many problem reports from this device; try again later")
	}
	c, hidden, err := s.repo.AddComplaint(ctx, ports.ComplaintParams{
		Complaint: moderation.Complaint{
			ID: uuid.New(), ReportID: reportID, DeviceID: in.DeviceID, Reason: in.Reason,
			Details: trimmed(in.Details), Status: moderation.StatusPending, CreatedAt: now,
		},
		Policy: s.policy,
		Now:    now,
	})
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, apperr.NotFound("report not found")
	}
	if hidden {
		log.Printf("moderation: report %s auto-hidden (threshold %d)", reportID, s.policy.AutoHideThreshold)
	}
	return c, nil
}

// Queue lists incidents with pending complaints, most reported first.
func (s *Service) Queue(ctx context.Context) ([]ports.ModerationQueueItem, error) {
	return s.repo.Queue(ctx, queueLimit)
}

// Apply records an operator decision on an incident.
func (s *Service) Apply(ctx context.Context, reportID uuid.UUID, action moderation.Action) error {
	if !action.Valid() {
		return apperr.Validation("moderation action is invalid", map[string]string{"action": "must be one of dismiss, hide, unhide"})
	}
	ok, err := s.repo.Apply(ctx, reportID, action, s.clock.Now())
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("report not found")
	}
	log.Printf("admin: moderation %s on report %s", action, reportID)
	return nil
}

func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}
