// Package follow contains the follow / in-app notification use cases.
package follow

import (
	"context"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	domainfollow "floodnow-api/internal/domain/follow"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

const (
	defaultNotificationLimit = 50
	// maxNotificationLookback bounds how far back a feed request can reach so
	// a client with no stored cursor doesn't scan the entire event history.
	maxNotificationLookback = 7 * 24 * time.Hour
)

type Service struct {
	follows ports.FollowRepository
	reports ports.ReportRepository
	clock   ports.Clock
}

func NewService(follows ports.FollowRepository, reports ports.ReportRepository, clock ports.Clock) *Service {
	return &Service{follows: follows, reports: reports, clock: clock}
}

func (s *Service) List(ctx context.Context, deviceID string) ([]domainfollow.Follow, error) {
	if err := domainfollow.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	return s.follows.ListByDevice(ctx, deviceID)
}

// Create adds a follow. Following the same report twice returns the
// existing follow instead of creating a duplicate.
func (s *Service) Create(ctx context.Context, in domainfollow.NewFollowInput) (*domainfollow.Follow, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	if in.Kind == domainfollow.KindReport {
		existing, err := s.follows.FindReportFollow(ctx, in.DeviceID, *in.ReportID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
		r, err := s.reports.GetByID(ctx, *in.ReportID)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, apperr.NotFound("report not found")
		}
	}

	count, err := s.follows.CountByDevice(ctx, in.DeviceID)
	if err != nil {
		return nil, err
	}
	if count >= domainfollow.MaxPerDevice {
		return nil, &apperr.Error{Code: apperr.CodeConflict, Message: "too many follows; remove one first"}
	}

	f := &domainfollow.Follow{
		ID:        uuid.New(),
		DeviceID:  in.DeviceID,
		Kind:      in.Kind,
		ReportID:  in.ReportID,
		Latitude:  in.Latitude,
		Longitude: in.Longitude,
		RadiusM:   in.RadiusM,
		CreatedAt: s.clock.Now(),
	}
	if in.Kind == domainfollow.KindReport {
		f.Latitude, f.Longitude, f.RadiusM = nil, nil, nil
	} else {
		f.ReportID = nil
	}
	if err := s.follows.Create(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *Service) Delete(ctx context.Context, deviceID string, id uuid.UUID) error {
	if err := domainfollow.ValidateDeviceID(deviceID); err != nil {
		return err
	}
	ok, err := s.follows.Delete(ctx, deviceID, id)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("follow not found")
	}
	return nil
}

// Notifications returns what happened to the device's follows since `since`
// (newest first). This is pull-based in-app delivery; no push is sent.
func (s *Service) Notifications(ctx context.Context, deviceID string, since *time.Time) ([]domainfollow.Notification, error) {
	if err := domainfollow.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	earliest := s.clock.Now().Add(-maxNotificationLookback)
	from := earliest
	if since != nil && since.After(earliest) {
		from = *since
	}

	candidates, err := s.follows.Notifications(ctx, ports.NotificationQuery{
		DeviceID:       deviceID,
		Since:          from,
		Limit:          defaultNotificationLimit,
		AreaSeverities: domainreport.SevereSeverities(),
	})
	if err != nil {
		return nil, err
	}

	out := make([]domainfollow.Notification, 0, len(candidates))
	seen := map[int64]bool{} // an event can match several follows; notify once
	for _, c := range candidates {
		kind, ok := domainfollow.NotificationKindFor(c.FollowKind, c.EventKind, c.Report.Severity)
		if !ok || seen[c.EventID] {
			continue
		}
		seen[c.EventID] = true
		out = append(out, domainfollow.Notification{
			EventID:   c.EventID,
			Kind:      kind,
			CreatedAt: c.CreatedAt,
			FollowID:  c.FollowID,
			Report:    c.Report,
		})
	}
	return out, nil
}
