package report

import (
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

// ReactionType is lightweight social feedback on a report — never severity,
// trust, freshness, route safety or moderation input (see docs/api-spec.md).
type ReactionType string

const (
	ReactionLike    ReactionType = "like"
	ReactionSupport ReactionType = "support"
)

func (t ReactionType) Valid() bool {
	switch t {
	case ReactionLike, ReactionSupport:
		return true
	}
	return false
}

// Reaction is one device's like/support on one report. A device has at most
// one: setting a new type switches it rather than adding another.
type Reaction struct {
	ID        uuid.UUID
	ReportID  uuid.UUID
	DeviceID  string
	Type      ReactionType
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewReactionInput is caller-provided input for setting a device's reaction.
type NewReactionInput struct {
	DeviceID string
	Type     ReactionType
}

func (in NewReactionInput) Validate() error {
	fields := map[string]string{}
	if !ValidDeviceID(in.DeviceID) {
		fields["device_id"] = "must be between 8 and 128 characters"
	}
	if !in.Type.Valid() {
		fields["type"] = "must be one of like, support"
	}
	if len(fields) > 0 {
		return apperr.Validation("reaction is invalid", fields)
	}
	return nil
}

// ValidDeviceID reports whether id is an acceptable anonymous device id.
func ValidDeviceID(id string) bool {
	return len(id) >= 8 && len(id) <= 128
}
