package report

import (
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

type ConfirmationStatus string

const (
	StatusStillActive ConfirmationStatus = "still_active"
	StatusCleared     ConfirmationStatus = "cleared"
)

func (s ConfirmationStatus) Valid() bool {
	switch s {
	case StatusStillActive, StatusCleared:
		return true
	}
	return false
}

type Confirmation struct {
	ID        uuid.UUID
	ReportID  uuid.UUID
	DeviceID  string
	Status    ConfirmationStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ConditionUpdate is what a device says the situation looks like now, sent
// with a still_active confirmation. Nil fields are left as they are; the
// provided ones replace the report's current values (anyone on the spot can
// correct the depth, severity, passability or photo — the report shows the
// latest known condition, not the original reporter's).
type ConditionUpdate struct {
	Severity    *Severity
	WaterDepth  *WaterDepth
	Passability *Passability
	ImageKey    *string
}

// Empty reports whether the update changes nothing.
func (u ConditionUpdate) Empty() bool {
	return u.Severity == nil && u.WaterDepth == nil && u.Passability == nil && u.ImageKey == nil
}

// For drops fields that don't apply to a report of type t, like
// NewReportInput.Normalized does on create.
func (u ConditionUpdate) For(t Type) ConditionUpdate {
	if t != TypeFlooded {
		u.WaterDepth = nil
	}
	if !t.AffectsRoad() {
		u.Passability = nil
	}
	return u
}

// NewConfirmationInput is caller-provided input for confirming a report.
type NewConfirmationInput struct {
	DeviceID string
	Status   ConfirmationStatus
	Update   ConditionUpdate
}

func (in NewConfirmationInput) Validate() error {
	fields := map[string]string{}

	if len(in.DeviceID) < 8 || len(in.DeviceID) > 128 {
		fields["device_id"] = "must be between 8 and 128 characters"
	}
	if !in.Status.Valid() {
		fields["status"] = "must be one of still_active, cleared"
	} else if in.Status != StatusStillActive && !in.Update.Empty() {
		fields["status"] = "must be still_active when the condition is updated"
	}
	u := in.Update
	if u.Severity != nil && !u.Severity.Valid() {
		fields["severity"] = "must be one of " + joinValues(validSeverities)
	}
	if u.WaterDepth != nil && !u.WaterDepth.Valid() {
		fields["water_depth"] = "must be one of unknown, ankle, shin, knee, above_knee"
	}
	validatePassability(u.Passability, fields)
	if u.ImageKey != nil && !validImageKey(*u.ImageKey) {
		fields["image_key"] = "must be a plain object key returned by /uploads/presign"
	}

	if len(fields) > 0 {
		return apperr.Validation("confirmation is invalid", fields)
	}
	return nil
}
