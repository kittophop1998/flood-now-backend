// Package report holds the FloodNow report entity and its business rules.
// It must not import Gin, database/sql, or any storage/HTTP SDK.
package report

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

type Type string

const (
	TypeFlooded        Type = "flooded"
	TypeRoadBlocked    Type = "road_blocked"
	TypeVehicleStalled Type = "vehicle_stalled"
	TypeHelpNeeded     Type = "help_needed"
)

func (t Type) Valid() bool {
	switch t {
	case TypeFlooded, TypeRoadBlocked, TypeVehicleStalled, TypeHelpNeeded:
		return true
	}
	return false
}

var validTypes = []string{
	string(TypeFlooded), string(TypeRoadBlocked), string(TypeVehicleStalled), string(TypeHelpNeeded),
}

type Severity string

const (
	SeverityPassable                  Severity = "passable"
	SeverityCaution                   Severity = "caution"
	SeveritySmallVehicleNotRecommended Severity = "small_vehicle_not_recommended"
	SeverityImpassable                Severity = "impassable"
)

func (s Severity) Valid() bool {
	switch s {
	case SeverityPassable, SeverityCaution, SeveritySmallVehicleNotRecommended, SeverityImpassable:
		return true
	}
	return false
}

var validSeverities = []string{
	string(SeverityPassable), string(SeverityCaution), string(SeveritySmallVehicleNotRecommended), string(SeverityImpassable),
}

// Report is the core entity. Optional fields are pointers so "not provided"
// is distinguishable from a zero value.
type Report struct {
	ID              uuid.UUID
	Type            Type
	Severity        Severity
	Latitude        float64
	Longitude       float64
	WaterLevelCM    *int
	Description     *string
	ImageKey        *string
	PeopleCount     *int
	HasChild        *bool
	HasElderly      *bool
	ContactPhone    *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastVerifiedAt  time.Time
	ExpiresAt       time.Time
}

// ReportWithStats is a Report plus derived/aggregated fields used for API
// responses. It lives in the domain because "is this expired" and the
// confirmation counts are read alongside the entity everywhere it's shown.
type ReportWithStats struct {
	Report
	StillActiveCount int
	ClearedCount     int
}

func (r ReportWithStats) IsExpired(now time.Time) bool {
	return !now.Before(r.ExpiresAt)
}

// NewReportInput is the set of caller-provided fields for creating a report.
type NewReportInput struct {
	Type         Type
	Severity     Severity
	Latitude     float64
	Longitude    float64
	WaterLevelCM *int
	Description  *string
	ImageKey     *string
	PeopleCount  *int
	HasChild     *bool
	HasElderly   *bool
	ContactPhone *string
}

// Validate checks all business invariants for a new report and returns a
// single aggregated *apperr.Error (code VALIDATION_ERROR) if any fail.
func (in NewReportInput) Validate() error {
	fields := map[string]string{}

	if !in.Type.Valid() {
		fields["type"] = "must be one of " + strings.Join(validTypes, ", ")
	}
	if !in.Severity.Valid() {
		fields["severity"] = "must be one of " + strings.Join(validSeverities, ", ")
	}
	if in.Latitude < -90 || in.Latitude > 90 {
		fields["latitude"] = "must be between -90 and 90"
	}
	if in.Longitude < -180 || in.Longitude > 180 {
		fields["longitude"] = "must be between -180 and 180"
	}
	if in.WaterLevelCM != nil && *in.WaterLevelCM < 0 {
		fields["water_level_cm"] = "must be >= 0"
	}
	if in.PeopleCount != nil && *in.PeopleCount < 0 {
		fields["people_count"] = "must be >= 0"
	}
	if in.ImageKey != nil {
		key := strings.TrimSpace(*in.ImageKey)
		if key == "" || strings.Contains(key, "..") || strings.Contains(key, "://") {
			fields["image_key"] = "must be a plain object key returned by /uploads/presign"
		}
	}
	if in.ContactPhone != nil && len(strings.TrimSpace(*in.ContactPhone)) > 32 {
		fields["contact_phone"] = "must be 32 characters or fewer"
	}
	if in.Description != nil && len(*in.Description) > 2000 {
		fields["description"] = "must be 2000 characters or fewer"
	}

	if len(fields) > 0 {
		return apperr.Validation("report is invalid", fields)
	}
	return nil
}
