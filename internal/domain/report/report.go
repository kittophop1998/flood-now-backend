// Package report holds the FloodNow report entity and its business rules.
// It must not import Gin, database/sql, or any storage/HTTP SDK.
package report

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

// Type is the incident category.
type Type string

const (
	TypeFlooded        Type = "flooded"
	TypeRoadClosed     Type = "road_closed"
	TypeAccident       Type = "accident"
	TypeVehicleStalled Type = "vehicle_stalled"
	TypeObstruction    Type = "obstruction"
	TypePowerOutage    Type = "power_outage"
	TypeHelpNeeded     Type = "help_needed"
	TypeShelter        Type = "shelter"
	TypeAidPoint       Type = "aid_point"
	TypeOther          Type = "other"
)

// typeRules is the single table of per-category behavior: whether the
// category describes road usability (so passability applies), whether it's
// a long-lived facility (shelter/aid point use the longer freshness window),
// how close a same-category report must be to count as a likely duplicate,
// and whether it's a legacy category that stored reports may still carry but
// new reports may not use (someone who needs help uses the SOS flow instead).
var typeRules = map[Type]struct {
	affectsRoad     bool
	facility        bool
	duplicateRadius float64 // meters
	legacy          bool
}{
	TypeFlooded:        {affectsRoad: true, duplicateRadius: 150},
	TypeRoadClosed:     {affectsRoad: true, duplicateRadius: 100},
	TypeAccident:       {affectsRoad: true, duplicateRadius: 75},
	TypeVehicleStalled: {affectsRoad: true, duplicateRadius: 50, legacy: true},
	TypeObstruction:    {affectsRoad: true, duplicateRadius: 50},
	TypePowerOutage:    {duplicateRadius: 150},
	TypeHelpNeeded:     {duplicateRadius: 50, legacy: true},
	TypeShelter:        {facility: true, duplicateRadius: 100},
	TypeAidPoint:       {facility: true, duplicateRadius: 100},
	TypeOther:          {duplicateRadius: 50, legacy: true},
}

// creatableTypes are the categories a new report may use, in display order.
var creatableTypes = []string{
	string(TypeFlooded), string(TypeRoadClosed), string(TypeAccident), string(TypeObstruction),
	string(TypePowerOutage), string(TypeShelter), string(TypeAidPoint),
}

// Valid reports whether t is a known category. Legacy categories stay valid
// so stored reports keep reading, filtering and confirming normally.
func (t Type) Valid() bool {
	_, ok := typeRules[t]
	return ok
}

// Creatable reports whether a new report may use t. Legacy categories
// (vehicle_stalled, help_needed, other) describe someone needing assistance
// and belong to the SOS flow.
func (t Type) Creatable() bool {
	r, ok := typeRules[t]
	return ok && !r.legacy
}

// AffectsRoad reports whether per-vehicle passability is meaningful for t.
func (t Type) AffectsRoad() bool { return typeRules[t].affectsRoad }

// IsFacility reports whether t is a long-lived place (shelter, aid point)
// rather than a fast-changing road condition.
func (t Type) IsFacility() bool { return typeRules[t].facility }

// DuplicateRadiusMeters is how close an existing active report of the same
// category must be for a new report to be flagged as a likely duplicate.
func (t Type) DuplicateRadiusMeters() float64 { return typeRules[t].duplicateRadius }

// MaxDuplicateRadiusMeters is the largest per-category duplicate radius.
func MaxDuplicateRadiusMeters() float64 {
	max := 0.0
	for _, r := range typeRules {
		if r.duplicateRadius > max {
			max = r.duplicateRadius
		}
	}
	return max
}

// Severity is a general "how bad is it" level, independent of passability.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityModerate Severity = "moderate"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

var severityRank = map[Severity]int{SeverityLow: 1, SeverityModerate: 2, SeverityHigh: 3, SeverityCritical: 4}

var validSeverities = []string{
	string(SeverityLow), string(SeverityModerate), string(SeverityHigh), string(SeverityCritical),
}

func (s Severity) Valid() bool {
	_, ok := severityRank[s]
	return ok
}

// Rank orders severities from 1 (low) to 4 (critical); 0 for invalid values.
func (s Severity) Rank() int { return severityRank[s] }

// IsSevere is the threshold for "severe new incident nearby" notifications.
func (s Severity) IsSevere() bool { return s.Rank() >= SeverityHigh.Rank() }

// SevereSeverities lists every severity for which IsSevere is true.
func SevereSeverities() []Severity {
	var out []Severity
	for s := range severityRank {
		if s.IsSevere() {
			out = append(out, s)
		}
	}
	return out
}

// WaterDepth is an approximate, body-relative flood depth — people can judge
// "knee-deep" at a glance, not "37 cm".
type WaterDepth string

const (
	WaterDepthUnknown   WaterDepth = "unknown"
	WaterDepthAnkle     WaterDepth = "ankle"      // < 10 cm
	WaterDepthShin      WaterDepth = "shin"       // 10–30 cm
	WaterDepthKnee      WaterDepth = "knee"       // 30–50 cm
	WaterDepthAboveKnee WaterDepth = "above_knee" // > 50 cm
)

func (d WaterDepth) Valid() bool {
	switch d {
	case WaterDepthUnknown, WaterDepthAnkle, WaterDepthShin, WaterDepthKnee, WaterDepthAboveKnee:
		return true
	}
	return false
}

// PassLevel says whether one class of road user can get through.
type PassLevel string

const (
	PassPassable       PassLevel = "passable"
	PassCaution        PassLevel = "caution"
	PassNotRecommended PassLevel = "not_recommended"
	PassImpassable     PassLevel = "impassable"
	PassUnknown        PassLevel = "unknown"
)

func (p PassLevel) Valid() bool {
	switch p {
	case PassPassable, PassCaution, PassNotRecommended, PassImpassable, PassUnknown:
		return true
	}
	return false
}

// Passability is road usability per vehicle class.
type Passability struct {
	Walk       PassLevel
	Motorcycle PassLevel
	Sedan      PassLevel
	SUVPickup  PassLevel
}

// GeometryType describes the report's affected geography. Only point reports
// are accepted today; the other values are reserved so the schema/API don't
// need a breaking change to support road segments or areas later.
type GeometryType string

const (
	GeometryPoint       GeometryType = "point"
	GeometryRoadSegment GeometryType = "road_segment"
	GeometryArea        GeometryType = "area"
)

// Report is the core entity. Optional fields are pointers so "not provided"
// is distinguishable from a zero value.
type Report struct {
	ID             uuid.UUID
	Type           Type
	Severity       Severity
	Latitude       float64
	Longitude      float64
	GeometryType   GeometryType
	WaterDepth     *WaterDepth
	WaterLevelCM   *int
	Passability    *Passability
	Description    *string
	ImageKey       *string
	PeopleCount    *int
	HasChild       *bool
	HasElderly     *bool
	ContactPhone   *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastVerifiedAt time.Time
	StaleAt        time.Time
	ExpiresAt      time.Time
	ResolvedAt     *time.Time
	// ClientID is an optional client-generated idempotency key (offline
	// queue): re-sending the same create returns the original report.
	ClientID *string
	// HiddenAt is set by moderation; hidden reports are excluded from every
	// public read.
	HiddenAt     *time.Time
	HiddenReason *HiddenReason
}

// HiddenReason records who hid a report.
type HiddenReason string

const (
	HiddenAutoThreshold HiddenReason = "auto_threshold" // enough distinct problem reports
	HiddenAdmin         HiddenReason = "admin"
)

// ReportWithStats is a Report plus derived/aggregated fields used for API
// responses. It lives in the domain because lifecycle status and the
// confirmation counts are read alongside the entity everywhere it's shown.
type ReportWithStats struct {
	Report
	StillActiveCount int
	ClearedCount     int
	// DistanceM is set only by location-based queries (nearby, duplicates).
	DistanceM *float64
}

func (r Report) IsExpired(now time.Time) bool {
	return !now.Before(r.ExpiresAt)
}

// NewReportInput is the set of caller-provided fields for creating a report.
type NewReportInput struct {
	Type         Type
	Severity     Severity
	Latitude     float64
	Longitude    float64
	GeometryType GeometryType
	WaterDepth   *WaterDepth
	WaterLevelCM *int
	Passability  *Passability
	Description  *string
	ImageKey     *string
	PeopleCount  *int
	HasChild     *bool
	HasElderly   *bool
	ContactPhone *string
	ClientID     *string
}

// ValidClientID reports whether id is an acceptable idempotency key.
func ValidClientID(id string) bool {
	return len(id) >= 8 && len(id) <= 64 && !strings.ContainsAny(id, " \t\n")
}

// Validate checks all business invariants for a new report and returns a
// single aggregated *apperr.Error (code VALIDATION_ERROR) if any fail.
func (in NewReportInput) Validate() error {
	fields := map[string]string{}

	if !in.Type.Creatable() {
		fields["type"] = "must be one of " + strings.Join(creatableTypes, ", ")
		if in.Type.Valid() {
			fields["type"] = "this category must use the SOS flow (POST /api/v1/sos); new reports " + fields["type"]
		}
	}
	if !in.Severity.Valid() {
		fields["severity"] = "must be one of " + joinValues(validSeverities)
	}
	if in.Latitude < -90 || in.Latitude > 90 {
		fields["latitude"] = "must be between -90 and 90"
	}
	if in.Longitude < -180 || in.Longitude > 180 {
		fields["longitude"] = "must be between -180 and 180"
	}
	if in.GeometryType != "" && in.GeometryType != GeometryPoint {
		fields["geometry_type"] = "only point reports are supported"
	}
	if in.WaterDepth != nil && !in.WaterDepth.Valid() {
		fields["water_depth"] = "must be one of unknown, ankle, shin, knee, above_knee"
	}
	if in.WaterLevelCM != nil && *in.WaterLevelCM < 0 {
		fields["water_level_cm"] = "must be >= 0"
	}
	validatePassability(in.Passability, fields)
	if in.PeopleCount != nil && *in.PeopleCount < 0 {
		fields["people_count"] = "must be >= 0"
	}
	if in.ImageKey != nil && !validImageKey(*in.ImageKey) {
		fields["image_key"] = "must be a plain object key returned by /uploads/presign"
	}
	if in.ContactPhone != nil && len(strings.TrimSpace(*in.ContactPhone)) > 32 {
		fields["contact_phone"] = "must be 32 characters or fewer"
	}
	if in.Description != nil && len(*in.Description) > 2000 {
		fields["description"] = "must be 2000 characters or fewer"
	}
	if in.ClientID != nil && !ValidClientID(*in.ClientID) {
		fields["client_id"] = "must be 8-64 characters without spaces"
	}

	if len(fields) > 0 {
		return apperr.Validation("report is invalid", fields)
	}
	return nil
}

func joinValues(values []string) string { return strings.Join(values, ", ") }

// validatePassability adds a field error for each invalid vehicle level.
func validatePassability(p *Passability, fields map[string]string) {
	if p == nil {
		return
	}
	for name, v := range map[string]PassLevel{"walk": p.Walk, "motorcycle": p.Motorcycle, "sedan": p.Sedan, "suv_pickup": p.SUVPickup} {
		if !v.Valid() {
			fields["passability."+name] = "must be one of passable, caution, not_recommended, impassable, unknown"
		}
	}
}

// validImageKey accepts only a plain object key such as /uploads/presign
// returns — never a URL or a path escaping the bucket prefix.
func validImageKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && !strings.Contains(key, "..") && !strings.Contains(key, "://")
}

// Normalized drops fields that don't apply to the report's category (water
// depth outside floods, passability for non-road incidents), so a client
// that switched category mid-form can't store contradictory data.
func (in NewReportInput) Normalized() NewReportInput {
	out := in
	if out.GeometryType == "" {
		out.GeometryType = GeometryPoint
	}
	if out.Type != TypeFlooded {
		out.WaterDepth = nil
		out.WaterLevelCM = nil
	}
	if !out.Type.AffectsRoad() {
		out.Passability = nil
	}
	return out
}
