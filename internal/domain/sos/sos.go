// Package sos holds SOS requests, helper profiles and the rules between
// them: which transitions are allowed for whom, and which helpers can answer
// which request. Matching is capability + distance + availability only.
//
// FloodNow is not a rescue service: an SOS is shown to opted-in community
// helpers nearby, never dispatched to officials.
package sos

import (
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

// Type is what kind of help is needed.
type Type string

const (
	TypeTrapped          Type = "trapped"
	TypeElderlyOrPatient Type = "elderly_or_patient"
	TypeVehicleStalled   Type = "vehicle_stalled"
	TypeNeedBoat         Type = "need_boat"
	TypeNeedHighVehicle  Type = "need_high_vehicle"
	TypeNeedFoodWater    Type = "need_food_water"
	TypeNeedShelter      Type = "need_shelter"
	TypeOther            Type = "other"
)

// Capability is something a helper can offer.
type Capability string

const (
	CapHighVehicle   Capability = "high_vehicle"
	CapBoat          Capability = "boat"
	CapFirstAid      Capability = "first_aid"
	CapFoodWater     Capability = "food_water"
	CapVehicleRepair Capability = "vehicle_repair"
	CapTowing        Capability = "towing"
	CapShelter       Capability = "shelter"
	CapOther         Capability = "other"
)

var allCapabilities = []Capability{CapHighVehicle, CapBoat, CapFirstAid, CapFoodWater, CapVehicleRepair, CapTowing, CapShelter, CapOther}

func (c Capability) Valid() bool { return slices.Contains(allCapabilities, c) }

// requiredCapabilities is the single matching table: a helper can answer a
// request when they have at least one of its capabilities. "other" requests
// are open to every helper.
var requiredCapabilities = map[Type][]Capability{
	TypeTrapped:          {CapBoat, CapHighVehicle},
	TypeElderlyOrPatient: {CapFirstAid, CapHighVehicle, CapBoat},
	TypeVehicleStalled:   {CapVehicleRepair, CapTowing},
	TypeNeedBoat:         {CapBoat},
	TypeNeedHighVehicle:  {CapHighVehicle},
	TypeNeedFoodWater:    {CapFoodWater},
	TypeNeedShelter:      {CapShelter},
	TypeOther:            allCapabilities,
}

func (t Type) Valid() bool {
	_, ok := requiredCapabilities[t]
	return ok
}

// RequiredCapabilities lists the capabilities that can answer t.
func (t Type) RequiredCapabilities() []Capability {
	return requiredCapabilities[t]
}

// CanAnswer reports whether a helper with caps can answer a request of type t.
func CanAnswer(t Type, caps []Capability) bool {
	for _, need := range requiredCapabilities[t] {
		if slices.Contains(caps, need) {
			return true
		}
	}
	return false
}

// Status is the SOS lifecycle.
type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusMatched   Status = "matched"
	StatusOnTheWay  Status = "on_the_way"
	StatusArrived   Status = "arrived"
	StatusCompleted Status = "completed"
	StatusCancelled Status = "cancelled"
)

func (s Status) Valid() bool {
	switch s {
	case StatusWaiting, StatusMatched, StatusOnTheWay, StatusArrived, StatusCompleted, StatusCancelled:
		return true
	}
	return false
}

// IsClosed reports a terminal status.
func (s Status) IsClosed() bool { return s == StatusCompleted || s == StatusCancelled }

// Role is how a device relates to a request.
type Role string

const (
	RoleRequester Role = "requester"
	RoleHelper    Role = "helper"
)

// transitions is the full state machine: from → to → who may do it.
// "waiting" as a target means the assigned helper withdrew and the request
// goes back to the pool. Accepting (waiting → matched) is a separate,
// atomic operation (see ports.SOSRepository.Accept) and not listed here.
var transitions = map[Status]map[Status][]Role{
	StatusWaiting: {
		StatusCancelled: {RoleRequester},
	},
	StatusMatched: {
		StatusOnTheWay:  {RoleHelper},
		StatusWaiting:   {RoleHelper},
		StatusCancelled: {RoleRequester},
	},
	StatusOnTheWay: {
		StatusArrived:   {RoleHelper},
		StatusWaiting:   {RoleHelper},
		StatusCancelled: {RoleRequester},
	},
	StatusArrived: {
		StatusCompleted: {RoleHelper, RoleRequester},
		StatusCancelled: {RoleRequester},
	},
}

// CanTransition reports whether role may move a request from → to.
func CanTransition(from, to Status, role Role) bool {
	return slices.Contains(transitions[from][to], role)
}

// Request is an SOS.
type Request struct {
	ID             uuid.UUID
	DeviceID       string
	ClientID       *string
	Type           Type
	Description    *string
	Latitude       float64
	Longitude      float64
	PeopleCount    *int
	ContactPhone   *string
	Status         Status
	HelperDeviceID *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ClosedAt       *time.Time
}

// RoleOf returns deviceID's role on the request, or "" if it has none.
func (r Request) RoleOf(deviceID string) Role {
	switch {
	case deviceID == "":
		return ""
	case r.DeviceID == deviceID:
		return RoleRequester
	case r.HelperDeviceID != nil && *r.HelperDeviceID == deviceID:
		return RoleHelper
	}
	return ""
}

// Event is one entry in a request's status timeline.
type Event struct {
	Status    Status
	Actor     Role
	CreatedAt time.Time
}

// MaxOpenAge is how long a waiting request stays visible to helpers. Older
// unanswered requests are most likely handled elsewhere; the requester can
// still see and cancel theirs.
const MaxOpenAge = 24 * time.Hour

// MaxActiveAssignments caps how many open requests one helper can hold, so
// nobody can claim a whole area's requests.
const MaxActiveAssignments = 3

// ValidateDeviceID mirrors the follow/confirmation rule for anonymous ids.
func ValidateDeviceID(deviceID string) error {
	if len(deviceID) < 8 || len(deviceID) > 128 {
		return apperr.Validation("device_id is invalid", map[string]string{"device_id": "must be between 8 and 128 characters"})
	}
	return nil
}

// NewRequestInput is what a requester sends.
type NewRequestInput struct {
	DeviceID     string
	ClientID     *string
	Type         Type
	Description  *string
	Latitude     float64
	Longitude    float64
	PeopleCount  *int
	ContactPhone *string
}

func (in NewRequestInput) Validate() error {
	fields := map[string]string{}
	if len(in.DeviceID) < 8 || len(in.DeviceID) > 128 {
		fields["device_id"] = "must be between 8 and 128 characters"
	}
	if in.ClientID != nil && (len(*in.ClientID) < 8 || len(*in.ClientID) > 64) {
		fields["client_id"] = "must be 8-64 characters"
	}
	if !in.Type.Valid() {
		fields["type"] = "must be one of trapped, elderly_or_patient, vehicle_stalled, need_boat, need_high_vehicle, need_food_water, need_shelter, other"
	}
	if in.Latitude < -90 || in.Latitude > 90 {
		fields["latitude"] = "must be between -90 and 90"
	}
	if in.Longitude < -180 || in.Longitude > 180 {
		fields["longitude"] = "must be between -180 and 180"
	}
	if in.PeopleCount != nil && (*in.PeopleCount < 1 || *in.PeopleCount > 500) {
		fields["people_count"] = "must be between 1 and 500"
	}
	if in.Description != nil && len([]rune(*in.Description)) > 1000 {
		fields["description"] = "must be 1000 characters or fewer"
	}
	if in.ContactPhone != nil && len(strings.TrimSpace(*in.ContactPhone)) > 32 {
		fields["contact_phone"] = "must be 32 characters or fewer"
	}
	if len(fields) > 0 {
		return apperr.Validation("sos request is invalid", fields)
	}
	return nil
}

// Helper is a device's helper-mode profile.
type Helper struct {
	DeviceID     string
	Active       bool
	Capabilities []Capability
	RadiusM      int
	DisplayName  *string
	ContactPhone *string
	Latitude     *float64
	Longitude    *float64
	LocationAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AllowedHelperRadiiM are the radii a helper can cover.
var AllowedHelperRadiiM = []int{1000, 3000, 5000, 10000}

// HelperLocationMaxAge is how long a helper's last known position counts for
// "helpers nearby" counts shown to requesters.
const HelperLocationMaxAge = 6 * time.Hour

// HelperInput is a full helper profile update.
type HelperInput struct {
	DeviceID     string
	Active       bool
	Capabilities []Capability
	RadiusM      int
	DisplayName  *string
	ContactPhone *string
	Latitude     *float64
	Longitude    *float64
}

func (in HelperInput) Validate() error {
	fields := map[string]string{}
	if len(in.DeviceID) < 8 || len(in.DeviceID) > 128 {
		fields["device_id"] = "must be between 8 and 128 characters"
	}
	seen := map[Capability]bool{}
	for _, c := range in.Capabilities {
		if !c.Valid() {
			fields["capabilities"] = "contains an unknown capability: " + string(c)
		}
		if seen[c] {
			fields["capabilities"] = "must not repeat a capability"
		}
		seen[c] = true
	}
	if in.Active && len(in.Capabilities) == 0 {
		fields["capabilities"] = "choose at least one capability to become active"
	}
	if !slices.Contains(AllowedHelperRadiiM, in.RadiusM) {
		fields["radius_m"] = "must be one of 1000, 3000, 5000, 10000"
	}
	if in.DisplayName != nil && len([]rune(strings.TrimSpace(*in.DisplayName))) > 60 {
		fields["display_name"] = "must be 60 characters or fewer"
	}
	if in.ContactPhone != nil && len(strings.TrimSpace(*in.ContactPhone)) > 32 {
		fields["contact_phone"] = "must be 32 characters or fewer"
	}
	if (in.Latitude == nil) != (in.Longitude == nil) {
		fields["latitude"] = "latitude and longitude must be sent together"
	} else if in.Latitude != nil && (*in.Latitude < -90 || *in.Latitude > 90 || *in.Longitude < -180 || *in.Longitude > 180) {
		fields["latitude"] = "must be a valid latitude/longitude"
	}
	if len(fields) > 0 {
		return apperr.Validation("helper profile is invalid", fields)
	}
	return nil
}

// ApproximateCoordinate rounds a coordinate to 3 decimals (~110 m) for the
// helper browse list; the exact point is only revealed after accepting.
func ApproximateCoordinate(v float64) float64 {
	const f = 1000.0
	if v < 0 {
		return -float64(int64(-v*f+0.5)) / f
	}
	return float64(int64(v*f+0.5)) / f
}

// Trimmed returns a trimmed copy of s, or nil when it is nil/blank.
func Trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}
