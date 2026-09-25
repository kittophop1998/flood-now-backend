package report

import (
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

type ConfirmationStatus string

const (
	StatusStillActive ConfirmationStatus = "still_active"
	StatusCleared      ConfirmationStatus = "cleared"
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

// NewConfirmationInput is caller-provided input for confirming a report.
type NewConfirmationInput struct {
	DeviceID string
	Status   ConfirmationStatus
}

func (in NewConfirmationInput) Validate() error {
	fields := map[string]string{}

	if len(in.DeviceID) < 8 || len(in.DeviceID) > 128 {
		fields["device_id"] = "must be between 8 and 128 characters"
	}
	if !in.Status.Valid() {
		fields["status"] = "must be one of still_active, cleared"
	}

	if len(fields) > 0 {
		return apperr.Validation("confirmation is invalid", fields)
	}
	return nil
}
