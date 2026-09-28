package report

import "floodnow-api/internal/domain/apperr"

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

// NewReactionInput is caller-provided input for setting a user's reaction.
// Reacting needs a signed-in user (one reaction per user per report; setting
// a new type switches it). Older anonymous per-device reactions still count
// toward the totals.
type NewReactionInput struct {
	Type ReactionType
}

func (in NewReactionInput) Validate() error {
	if !in.Type.Valid() {
		return apperr.Validation("reaction is invalid", map[string]string{"type": "must be one of like, support"})
	}
	return nil
}

// ValidDeviceID reports whether id is an acceptable anonymous device id.
func ValidDeviceID(id string) bool {
	return len(id) >= 8 && len(id) <= 128
}
