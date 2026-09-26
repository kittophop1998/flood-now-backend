// Package follow holds what an anonymous device follows (an area, a single
// report, or a named saved place with an optional watch area) and the
// notification items derived from report events. There is no push delivery
// yet: the same matching feeds the in-app notification list today and is
// what a push worker would consume later.
package follow

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/report"
)

type Kind string

const (
	KindArea   Kind = "area"
	KindReport Kind = "report"
	// KindPlace is a saved place (home, work…). Its radius is the watch area;
	// Notify switches its alerts on or off without deleting the place.
	KindPlace Kind = "place"
)

// AllowedRadiiM are the area-follow radii offered to users.
var AllowedRadiiM = []int{1000, 3000, 5000}

// MaxPerDevice caps area/report follows per device so one client can't make
// the notification matching query arbitrarily expensive.
const MaxPerDevice = 20

// MaxPlacesPerDevice caps saved places per device, for the same reason.
const MaxPlacesPerDevice = 10

type Follow struct {
	ID        uuid.UUID
	DeviceID  string
	Kind      Kind
	ReportID  *uuid.UUID
	Latitude  *float64
	Longitude *float64
	RadiusM   *int
	CreatedAt time.Time

	// Saved-place fields (KindPlace only).
	Name             *string
	Icon             *PlaceIcon
	PreferredVehicle *string
	Notify           bool
	UpdatedAt        time.Time
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

// PlaceIcon is the kind of saved place, shown as its icon.
type PlaceIcon string

const (
	IconHome   PlaceIcon = "home"
	IconWork   PlaceIcon = "work"
	IconFamily PlaceIcon = "family"
	IconCustom PlaceIcon = "custom"
)

func (i PlaceIcon) Valid() bool {
	switch i {
	case IconHome, IconWork, IconFamily, IconCustom:
		return true
	}
	return false
}

func validVehicle(v string) bool {
	switch v {
	case "walk", "motorcycle", "sedan", "suv_pickup":
		return true
	}
	return false
}

// PlaceFields is the editable part of a saved place. On update, nil fields
// are left unchanged.
type PlaceFields struct {
	Name             *string
	Icon             *PlaceIcon
	Latitude         *float64
	Longitude        *float64
	RadiusM          *int
	PreferredVehicle *string // "" clears it
	Notify           *bool
}

// Validate checks the fields that are present; requireAll demands every
// field a new place needs.
func (p PlaceFields) Validate(requireAll bool) error {
	fields := map[string]string{}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" || len([]rune(n)) > 60 {
			fields["name"] = "must be 1-60 characters"
		}
	} else if requireAll {
		fields["name"] = "is required"
	}
	if p.Icon != nil {
		if !p.Icon.Valid() {
			fields["icon"] = "must be one of home, work, family, custom"
		}
	} else if requireAll {
		fields["icon"] = "is required"
	}
	if (p.Latitude == nil) != (p.Longitude == nil) {
		fields["latitude"] = "latitude and longitude must be sent together"
	}
	if p.Latitude != nil && (*p.Latitude < -90 || *p.Latitude > 90) {
		fields["latitude"] = "must be between -90 and 90"
	}
	if p.Longitude != nil && (*p.Longitude < -180 || *p.Longitude > 180) {
		fields["longitude"] = "must be between -180 and 180"
	}
	if requireAll && p.Latitude == nil {
		fields["latitude"] = "is required"
	}
	if p.RadiusM != nil && !allowedRadius(*p.RadiusM) {
		fields["watch_radius_m"] = "must be one of 1000, 3000, 5000"
	}
	if p.PreferredVehicle != nil && *p.PreferredVehicle != "" && !validVehicle(*p.PreferredVehicle) {
		fields["preferred_vehicle"] = "must be one of walk, motorcycle, sedan, suv_pickup"
	}
	if len(fields) > 0 {
		return apperr.Validation("saved place is invalid", fields)
	}
	return nil
}

// Apply writes the present fields onto a saved place.
func (p PlaceFields) Apply(f *Follow) {
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		f.Name = &n
	}
	if p.Icon != nil {
		f.Icon = p.Icon
	}
	if p.Latitude != nil && p.Longitude != nil {
		f.Latitude, f.Longitude = p.Latitude, p.Longitude
	}
	if p.RadiusM != nil {
		f.RadiusM = p.RadiusM
	}
	if p.PreferredVehicle != nil {
		if *p.PreferredVehicle == "" {
			f.PreferredVehicle = nil
		} else {
			v := *p.PreferredVehicle
			f.PreferredVehicle = &v
		}
	}
	if p.Notify != nil {
		f.Notify = *p.Notify
	}
}

// AreaLevel summarizes open incidents inside a watch area.
type AreaLevel string

const (
	AreaClear   AreaLevel = "clear"   // no open reports
	AreaCaution AreaLevel = "caution" // open reports, none severe
	AreaSevere  AreaLevel = "severe"  // at least one open high/critical report
)

// AreaSummary is the current state of a saved place's watch area.
type AreaSummary struct {
	ActiveCount    int
	SevereCount    int
	LatestUpdateAt *time.Time
}

func (s AreaSummary) Level() AreaLevel {
	switch {
	case s.SevereCount > 0:
		return AreaSevere
	case s.ActiveCount > 0:
		return AreaCaution
	}
	return AreaClear
}

// PlaceWithSummary is a saved place plus its watch-area state.
type PlaceWithSummary struct {
	Follow
	Summary AreaSummary
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
	NotifyUpdated      NotificationKind = "updated"       // followed report's condition (depth, severity, passability, photo) changed
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
	case KindPlace:
		// A watched place also hears about a severe incident becoming active
		// again; routine confirmations never notify an area, to avoid spam.
		if (event == report.EventCreated || event == report.EventReopened) && severity.IsSevere() {
			return NotifySevereNearby, true
		}
	case KindReport:
		switch event {
		case report.EventConfirmed:
			return NotifyConfirmed, true
		case report.EventUpdated:
			return NotifyUpdated, true
		case report.EventResolved:
			return NotifyResolved, true
		case report.EventReopened:
			return NotifyReopened, true
		}
	}
	return "", false
}
