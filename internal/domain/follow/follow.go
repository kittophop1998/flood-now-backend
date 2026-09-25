// Package follow holds what an anonymous device follows (an area or a single
// report) and the notification items derived from report events. There is
// no push delivery yet: the same matching feeds the in-app notification list
// today and is what a push worker would consume later.
package follow

import (
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/report"
)

type Kind string

const (
	KindArea   Kind = "area"
	KindReport Kind = "report"
)

// AllowedRadiiM are the area-follow radii offered to users.
var AllowedRadiiM = []int{1000, 3000, 5000}

// MaxPerDevice caps follows per device so one client can't make the
// notification matching query arbitrarily expensive.
const MaxPerDevice = 20

type Follow struct {
	ID        uuid.UUID
	DeviceID  string
	Kind      Kind
	ReportID  *uuid.UUID
	Latitude  *float64
	Longitude *float64
	RadiusM   *int
	CreatedAt time.Time
}

type NewFollowInput struct {
	DeviceID  string
	Kind      Kind
	ReportID  *uuid.UUID
	Latitude  *float64
	Longitude *float64
	RadiusM   *int
}

func ValidateDeviceID(deviceID string) error {
	if len(deviceID) < 8 || len(deviceID) > 128 {
		return apperr.Validation("device_id is invalid", map[string]string{"device_id": "must be between 8 and 128 characters"})
	}
	return nil
}

func (in NewFollowInput) Validate() error {
	fields := map[string]string{}
	if len(in.DeviceID) < 8 || len(in.DeviceID) > 128 {
		fields["device_id"] = "must be between 8 and 128 characters"
	}
	switch in.Kind {
	case KindReport:
		if in.ReportID == nil {
			fields["report_id"] = "is required for report follows"
		}
	case KindArea:
		if in.Latitude == nil || *in.Latitude < -90 || *in.Latitude > 90 {
			fields["latitude"] = "must be between -90 and 90"
		}
		if in.Longitude == nil || *in.Longitude < -180 || *in.Longitude > 180 {
			fields["longitude"] = "must be between -180 and 180"
		}
		if in.RadiusM == nil || !allowedRadius(*in.RadiusM) {
			fields["radius_m"] = "must be one of 1000, 3000, 5000"
		}
	default:
		fields["kind"] = "must be one of area, report"
	}
	if len(fields) > 0 {
		return apperr.Validation("follow is invalid", fields)
	}
	return nil
}

func allowedRadius(r int) bool {
	for _, a := range AllowedRadiiM {
		if a == r {
			return true
		}
	}
	return false
}

// NotificationKind is what the user is told happened.
type NotificationKind string

const (
	NotifySevereNearby NotificationKind = "severe_nearby" // new severe report inside a followed area
	NotifyConfirmed    NotificationKind = "confirmed"     // followed report confirmed still active
	NotifyResolved     NotificationKind = "resolved"      // followed report resolved
	NotifyReopened     NotificationKind = "reopened"      // followed report reported active again
)

type Notification struct {
	EventID   int64
	Kind      NotificationKind
	CreatedAt time.Time
	FollowID  uuid.UUID
	Report    report.ReportWithStats
}

// NotificationKindFor maps a report event seen through a follow to what the
// user is notified about; ok is false when that follow doesn't care.
func NotificationKindFor(followKind Kind, event report.EventKind, severity report.Severity) (NotificationKind, bool) {
	switch followKind {
	case KindArea:
		if event == report.EventCreated && severity.IsSevere() {
			return NotifySevereNearby, true
		}
	case KindReport:
		switch event {
		case report.EventConfirmed:
			return NotifyConfirmed, true
		case report.EventResolved:
			return NotifyResolved, true
		case report.EventReopened:
			return NotifyReopened, true
		}
	}
	return "", false
}
