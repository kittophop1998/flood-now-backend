// Package announcement holds official announcements entered manually by an
// operator (structured fields only, nothing is parsed or generated). They are
// shown apart from community reports and always carry their source.
package announcement

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/report"
	"floodnow-api/internal/domain/upload"
)

type Type string

const (
	TypeFloodWarning Type = "flood_warning"
	TypeEvacuation   Type = "evacuation"
	TypeRoadClosure  Type = "road_closure"
	TypeWaterRelease Type = "water_release"
	TypeWeather      Type = "weather"
	TypeShelterInfo  Type = "shelter_info"
	TypeConstruction Type = "construction"
	TypeSafetyNotice Type = "safety_notice" // local safety / emergency notice
	TypeGeneral      Type = "general"

	TypeTrafficNotice     Type = "traffic_notice"
	TypeAccidentEmergency Type = "accident_emergency"
	TypePowerUtility      Type = "power_utility" // power / water supply / utility interruption
	TypeServiceDisruption Type = "service_disruption"
)

var types = []Type{
	TypeFloodWarning, TypeEvacuation, TypeRoadClosure, TypeWaterRelease, TypeWeather, TypeShelterInfo,
	TypeConstruction, TypeSafetyNotice, TypeGeneral,
	TypeTrafficNotice, TypeAccidentEmergency, TypePowerUtility, TypeServiceDisruption,
}

func (t Type) Valid() bool {
	for _, v := range types {
		if t == v {
			return true
		}
	}
	return false
}

// SeverityInfo is an announcement-only level below the report scale: a
// notice that is worth knowing but not a hazard (a service change, a
// general notice). Reports never use it.
const SeverityInfo report.Severity = "info"

func validSeverity(s report.Severity) bool { return s == SeverityInfo || s.Valid() }

// MaxImages caps the images attached to one announcement.
const MaxImages = 5

// Image is an attached photo: an object key minted by the admin presign
// endpoint (upload.AnnouncementKeyPrefix), plus optional pixel size so
// clients can lay it out before it loads. Order in the slice = display
// order, the first is the cover.
type Image struct {
	Key    string `json:"key"`
	Width  *int   `json:"width,omitempty"`
	Height *int   `json:"height,omitempty"`
}

// validImageKey accepts only a plain announcement object key — never a URL,
// a report photo key, or a path escaping the prefix.
func validImageKey(key string) bool {
	return strings.HasPrefix(key, upload.AnnouncementKeyPrefix) && len(key) <= 300 &&
		!strings.Contains(key, "..") && !strings.Contains(key, "://") && strings.TrimSpace(key) == key
}

func validateImages(images []Image, fields map[string]string) {
	if len(images) > MaxImages {
		fields["images"] = "at most " + strconv.Itoa(MaxImages) + " images"
		return
	}
	seen := map[string]bool{}
	for i, img := range images {
		name := "images." + strconv.Itoa(i)
		switch {
		case !validImageKey(img.Key):
			fields[name] = "image_key must be an object key returned by /admin/uploads/presign"
		case seen[img.Key]:
			fields[name] = "duplicate image"
		case img.Width != nil && (*img.Width < 1 || *img.Width > 20000),
			img.Height != nil && (*img.Height < 1 || *img.Height > 20000):
			fields[name] = "width/height must be 1-20000"
		}
		seen[img.Key] = true
	}
}

// Status is derived from publication and the time window, never stored.
type Status string

const (
	StatusDraft     Status = "draft"     // not published (admin only)
	StatusScheduled Status = "scheduled" // published, starts_at in the future
	StatusActive    Status = "active"
	StatusExpired   Status = "expired"
)

type Announcement struct {
	ID          uuid.UUID
	Title       string
	Body        string
	Type        Type
	Severity    report.Severity
	SourceName  string
	SourceURL   *string
	Latitude    *float64
	Longitude   *float64
	RadiusM     *int
	Images      []Image
	StartsAt    time.Time
	EndsAt      *time.Time
	PublishedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (a Announcement) Status(now time.Time) Status {
	switch {
	case a.PublishedAt == nil:
		return StatusDraft
	case a.EndsAt != nil && !now.Before(*a.EndsAt):
		return StatusExpired
	case now.Before(a.StartsAt):
		return StatusScheduled
	}
	return StatusActive
}

// ExpiredLookback is how long an ended announcement stays listable when the
// client asks for expired ones too.
const ExpiredLookback = 7 * 24 * time.Hour

// Fields is the editable part of an announcement; nil = unchanged on update.
// ClearLocation removes the affected area; a non-nil empty Images removes
// every image.
type Fields struct {
	Title         *string
	Body          *string
	Type          *Type
	Severity      *report.Severity
	SourceName    *string
	SourceURL     *string
	Latitude      *float64
	Longitude     *float64
	RadiusM       *int
	ClearLocation bool
	Images        *[]Image
	StartsAt      *time.Time
	EndsAt        *time.Time
	ClearEndsAt   bool
}

func textLen(s *string) int { return len([]rune(strings.TrimSpace(*s))) }

func (f Fields) Validate(requireAll bool) error {
	fields := map[string]string{}
	check := func(name string, v *string, max int) {
		if v != nil {
			if n := textLen(v); n < 1 || n > max {
				fields[name] = "must be 1-" + strconv.Itoa(max) + " characters"
			}
		} else if requireAll {
			fields[name] = "is required"
		}
	}
	check("title", f.Title, 200)
	check("body", f.Body, 5000)
	check("source_name", f.SourceName, 200)
	if f.Type != nil {
		if !f.Type.Valid() {
			names := make([]string, len(types))
			for i, v := range types {
				names[i] = string(v)
			}
			fields["type"] = "must be one of " + strings.Join(names, ", ")
		}
	} else if requireAll {
		fields["type"] = "is required"
	}
	if f.Severity != nil {
		if !validSeverity(*f.Severity) {
			fields["severity"] = "must be one of info, low, moderate, high, critical"
		}
	} else if requireAll {
		fields["severity"] = "is required"
	}
	if f.SourceURL != nil && strings.TrimSpace(*f.SourceURL) != "" {
		u, err := url.Parse(strings.TrimSpace(*f.SourceURL))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(*f.SourceURL) > 500 {
			fields["source_url"] = "must be an http(s) URL of at most 500 characters"
		}
	}
	if (f.Latitude == nil) != (f.Longitude == nil) {
		fields["latitude"] = "latitude and longitude must be sent together"
	} else if f.Latitude != nil && (*f.Latitude < -90 || *f.Latitude > 90 || *f.Longitude < -180 || *f.Longitude > 180) {
		fields["latitude"] = "must be a valid latitude/longitude"
	}
	if f.RadiusM != nil && (*f.RadiusM < 100 || *f.RadiusM > 200_000) {
		fields["radius_m"] = "must be between 100 and 200000"
	}
	if f.Images != nil {
		validateImages(*f.Images, fields)
	}
	if f.StartsAt == nil && requireAll {
		fields["starts_at"] = "is required"
	}
	if len(fields) > 0 {
		return apperr.Validation("announcement is invalid", fields)
	}
	return nil
}

// Apply writes the present fields onto a, then checks the combined result
// (a radius needs a point, the window must end after it starts).
func (f Fields) Apply(a *Announcement) error {
	if f.Title != nil {
		a.Title = strings.TrimSpace(*f.Title)
	}
	if f.Body != nil {
		a.Body = strings.TrimSpace(*f.Body)
	}
	if f.Type != nil {
		a.Type = *f.Type
	}
	if f.Severity != nil {
		a.Severity = *f.Severity
	}
	if f.SourceName != nil {
		a.SourceName = strings.TrimSpace(*f.SourceName)
	}
	if f.SourceURL != nil {
		if u := strings.TrimSpace(*f.SourceURL); u == "" {
			a.SourceURL = nil
		} else {
			a.SourceURL = &u
		}
	}
	if f.ClearLocation {
		a.Latitude, a.Longitude, a.RadiusM = nil, nil, nil
	}
	if f.Latitude != nil && f.Longitude != nil {
		a.Latitude, a.Longitude = f.Latitude, f.Longitude
	}
	if f.RadiusM != nil {
		a.RadiusM = f.RadiusM
	}
	if f.Images != nil {
		a.Images = append([]Image{}, *f.Images...)
	}
	if f.StartsAt != nil {
		a.StartsAt = f.StartsAt.UTC()
	}
	if f.ClearEndsAt {
		a.EndsAt = nil
	}
	if f.EndsAt != nil {
		e := f.EndsAt.UTC()
		a.EndsAt = &e
	}

	fields := map[string]string{}
	if a.RadiusM != nil && a.Latitude == nil {
		fields["radius_m"] = "needs latitude/longitude"
	}
	if a.EndsAt != nil && !a.EndsAt.After(a.StartsAt) {
		fields["ends_at"] = "must be after starts_at"
	}
	if len(fields) > 0 {
		return apperr.Validation("announcement is invalid", fields)
	}
	return nil
}
