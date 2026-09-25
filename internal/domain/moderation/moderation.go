// Package moderation holds "report a problem" complaints about incidents and
// the deterministic rules applied to them. There is no automated content
// analysis: only counts of distinct devices and explicit operator actions.
package moderation

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

type Reason string

const (
	ReasonFalseInformation   Reason = "false_information"
	ReasonWrongLocation      Reason = "wrong_location"
	ReasonDuplicate          Reason = "duplicate"
	ReasonOutdated           Reason = "outdated"
	ReasonInappropriateImage Reason = "inappropriate_image"
	ReasonSpam               Reason = "spam"
	ReasonPrivacy            Reason = "privacy"
	ReasonOther              Reason = "other"
)

func (r Reason) Valid() bool {
	switch r {
	case ReasonFalseInformation, ReasonWrongLocation, ReasonDuplicate, ReasonOutdated,
		ReasonInappropriateImage, ReasonSpam, ReasonPrivacy, ReasonOther:
		return true
	}
	return false
}

type Status string

const (
	StatusPending   Status = "pending"
	StatusResolved  Status = "resolved"  // an operator acted (hid / restored the report)
	StatusDismissed Status = "dismissed" // an operator found nothing wrong
)

type Complaint struct {
	ID         uuid.UUID
	ReportID   uuid.UUID
	DeviceID   string
	Reason     Reason
	Details    *string
	Status     Status
	CreatedAt  time.Time
	ResolvedAt *time.Time
}

// Policy is the deterministic moderation configuration.
type Policy struct {
	// AutoHideThreshold: a report is hidden pending review once this many
	// distinct devices have pending complaints about it.
	AutoHideThreshold int
	// MaxPerDevicePerHour caps how many complaints one device can file.
	MaxPerDevicePerHour int
}

func (p Policy) Validate() error {
	if p.AutoHideThreshold < 2 {
		return apperr.Internal("moderation auto-hide threshold must be >= 2")
	}
	if p.MaxPerDevicePerHour < 1 {
		return apperr.Internal("moderation per-device limit must be >= 1")
	}
	return nil
}

// ShouldAutoHide applies the threshold rule to the pending complaint count.
func (p Policy) ShouldAutoHide(pendingDistinctDevices int) bool {
	return pendingDistinctDevices >= p.AutoHideThreshold
}

type NewComplaintInput struct {
	DeviceID string
	Reason   Reason
	Details  *string
}

func (in NewComplaintInput) Validate() error {
	fields := map[string]string{}
	if len(in.DeviceID) < 8 || len(in.DeviceID) > 128 {
		fields["device_id"] = "must be between 8 and 128 characters"
	}
	if !in.Reason.Valid() {
		fields["reason"] = "must be one of false_information, wrong_location, duplicate, outdated, inappropriate_image, spam, privacy, other"
	}
	if in.Details != nil && len([]rune(strings.TrimSpace(*in.Details))) > 1000 {
		fields["details"] = "must be 1000 characters or fewer"
	}
	if len(fields) > 0 {
		return apperr.Validation("problem report is invalid", fields)
	}
	return nil
}

// Action is an operator decision on a reported incident.
type Action string

const (
	ActionDismiss Action = "dismiss" // complaints unfounded: dismiss them and show the report again
	ActionHide    Action = "hide"    // hide the report and resolve the complaints
	ActionUnhide  Action = "unhide"  // show a hidden report again and resolve the complaints
)

func (a Action) Valid() bool {
	return a == ActionDismiss || a == ActionHide || a == ActionUnhide
}

// Outcome is what an action does to the report and its pending complaints.
func (a Action) Outcome() (hide bool, complaintStatus Status) {
	switch a {
	case ActionHide:
		return true, StatusResolved
	case ActionUnhide:
		return false, StatusResolved
	}
	return false, StatusDismissed
}
