// Package event holds community events (temple fairs, markets, walking
// streets, concerts…): public, user-owned map content. It is a separate
// domain from incident reports — events are planned, have a start/end, are
// never confirmed or voted on and never feed route safety or alerts.
package event

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/upload"
)

type Category string

const (
	CategoryTempleFair    Category = "temple_fair"
	CategoryMarket        Category = "market"
	CategoryFair          Category = "fair"
	CategoryWalkingStreet Category = "walking_street"
	CategoryCommunity     Category = "community"
	CategoryFestival      Category = "festival"
	CategoryConcert       Category = "concert"
	CategoryOther         Category = "other"
)

var categories = []string{
	string(CategoryTempleFair), string(CategoryMarket), string(CategoryFair), string(CategoryWalkingStreet),
	string(CategoryCommunity), string(CategoryFestival), string(CategoryConcert), string(CategoryOther),
}

func (c Category) Valid() bool {
	for _, v := range categories {
		if string(c) == v {
			return true
		}
	}
	return false
}

// StoredStatus is what the owner controls; Status adds the time-derived
// "ended".
type StoredStatus string

const (
	StoredActive    StoredStatus = "active"
	StoredCancelled StoredStatus = "cancelled"
)

// Status is the lifecycle shown to clients.
type Status string

const (
	StatusActive    Status = "active"
	StatusCancelled Status = "cancelled"
	StatusEnded     Status = "ended"
)

const (
	MaxDuration = 31 * 24 * time.Hour
	// MaxLeadTime bounds how far ahead an event can be announced.
	MaxLeadTime = 366 * 24 * time.Hour
	// MaxUpcomingPerOwner caps a user's active, not-yet-ended events.
	MaxUpcomingPerOwner = 20
)

type Event struct {
	ID           uuid.UUID
	OwnerUserID  uuid.UUID
	Title        string
	Description  *string
	Category     Category
	Latitude     float64
	Longitude    float64
	LocationName *string
	StartAt      time.Time
	EndAt        time.Time
	ImageKey     *string
	StoredStatus StoredStatus
	CancelledAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// OrganizerName is the owner's public display name (read-only, joined
	// on read). Nothing else about the owner is ever exposed.
	OrganizerName string
}

// Status derives the lifecycle: cancelled wins, then ended once end_at has
// passed, otherwise active (upcoming or ongoing — see IsOngoing).
func (e Event) Status(now time.Time) Status {
	switch {
	case e.StoredStatus == StoredCancelled:
		return StatusCancelled
	case !now.Before(e.EndAt):
		return StatusEnded
	default:
		return StatusActive
	}
}

// IsOwnedBy reports whether userID owns the event.
func (e Event) IsOwnedBy(userID uuid.UUID) bool { return e.OwnerUserID == userID }

// Fields is create/update input; nil means "not sent" (update keeps the
// current value). ClearImage / clearing via "" removes optional text.
type Fields struct {
	Title        *string
	Description  *string
	Category     *Category
	Latitude     *float64
	Longitude    *float64
	LocationName *string
	StartAt      *time.Time
	EndAt        *time.Time
	ImageKey     *string
}

// ValidateCreate requires every mandatory field.
func (f Fields) ValidateCreate(now time.Time) error {
	fields := map[string]string{}
	if f.Title == nil {
		fields["title"] = "is required"
	}
	if f.Category == nil {
		fields["category"] = "is required"
	}
	if f.Latitude == nil {
		fields["latitude"] = "is required"
	}
	if f.Longitude == nil {
		fields["longitude"] = "is required"
	}
	if f.StartAt == nil {
		fields["start_at"] = "is required"
	}
	if f.EndAt == nil {
		fields["end_at"] = "is required"
	}
	f.validateValues(fields)
	if f.StartAt != nil && f.EndAt != nil {
		validateWindow(*f.StartAt, *f.EndAt, now, fields)
	}
	if len(fields) > 0 {
		return apperr.Validation("event is invalid", fields)
	}
	return nil
}

// Apply validates f as an update of e and writes the sent fields into it.
func (f Fields) Apply(e *Event, now time.Time) error {
	fields := map[string]string{}
	f.validateValues(fields)
	start, end := e.StartAt, e.EndAt
	if f.StartAt != nil {
		start = *f.StartAt
	}
	if f.EndAt != nil {
		end = *f.EndAt
	}
	if f.StartAt != nil || f.EndAt != nil {
		validateWindow(start, end, now, fields)
	}
	if len(fields) > 0 {
		return apperr.Validation("event is invalid", fields)
	}
	if f.Title != nil {
		e.Title = strings.TrimSpace(*f.Title)
	}
	if f.Description != nil {
		e.Description = trimmedOrNil(*f.Description)
	}
	if f.Category != nil {
		e.Category = *f.Category
	}
	if f.Latitude != nil {
		e.Latitude = *f.Latitude
	}
	if f.Longitude != nil {
		e.Longitude = *f.Longitude
	}
	if f.LocationName != nil {
		e.LocationName = trimmedOrNil(*f.LocationName)
	}
	if f.ImageKey != nil {
		e.ImageKey = trimmedOrNil(*f.ImageKey)
	}
	e.StartAt, e.EndAt = start.UTC(), end.UTC()
	return nil
}

func (f Fields) validateValues(fields map[string]string) {
	if f.Title != nil {
		if n := utf8.RuneCountInString(strings.TrimSpace(*f.Title)); n < 1 || n > 120 {
			fields["title"] = "must be between 1 and 120 characters"
		}
	}
	if f.Description != nil && utf8.RuneCountInString(*f.Description) > 2000 {
		fields["description"] = "must be 2000 characters or fewer"
	}
	if f.Category != nil && !f.Category.Valid() {
		fields["category"] = "must be one of " + strings.Join(categories, ", ")
	}
	if f.Latitude != nil && (*f.Latitude < -90 || *f.Latitude > 90) {
		fields["latitude"] = "must be between -90 and 90"
	}
	if f.Longitude != nil && (*f.Longitude < -180 || *f.Longitude > 180) {
		fields["longitude"] = "must be between -180 and 180"
	}
	if f.LocationName != nil && utf8.RuneCountInString(*f.LocationName) > 200 {
		fields["location_name"] = "must be 200 characters or fewer"
	}
	// "" clears the photo; otherwise it must be a key from the public
	// presign (events reuse the report photo upload flow).
	if f.ImageKey != nil && *f.ImageKey != "" && !validImageKey(*f.ImageKey) {
		fields["image_key"] = "must be an object key returned by /uploads/presign"
	}
}

func validateWindow(start, end, now time.Time, fields map[string]string) {
	switch {
	case !end.After(start):
		fields["end_at"] = "must be after start_at"
	case end.Sub(start) > MaxDuration:
		fields["end_at"] = "an event can last at most 31 days"
	case !end.After(now):
		fields["end_at"] = "must be in the future"
	case start.Sub(now) > MaxLeadTime:
		fields["start_at"] = "must be within a year from now"
	}
}

func validImageKey(key string) bool {
	return strings.HasPrefix(key, upload.ReportKeyPrefix) && !strings.Contains(key, "..") && !strings.Contains(key, "://")
}

func trimmedOrNil(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
