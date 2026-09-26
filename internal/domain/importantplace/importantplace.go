// Package importantplace holds emergency / important places (hospitals,
// shelters, boat points…), either curated by an operator or added by anyone
// from the app. Unlike community reports they don't expire; their status is
// set explicitly.
package importantplace

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

type Category string

const (
	CategoryHospital      Category = "hospital"
	CategoryShelter       Category = "shelter"
	CategoryFoodWater     Category = "food_water"
	CategoryRescue        Category = "rescue"
	CategoryPolice        Category = "police"
	CategoryFuel          Category = "fuel"
	CategoryVehicleRepair Category = "vehicle_repair"
	CategoryBoatPoint     Category = "boat_point"
	CategoryOther         Category = "other"
)

func (c Category) Valid() bool {
	switch c {
	case CategoryHospital, CategoryShelter, CategoryFoodWater, CategoryRescue, CategoryPolice,
		CategoryFuel, CategoryVehicleRepair, CategoryBoatPoint, CategoryOther:
		return true
	}
	return false
}

type Status string

const (
	StatusOpen    Status = "open"
	StatusClosed  Status = "closed"
	StatusFull    Status = "full"
	StatusUnknown Status = "unknown"
)

func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusClosed, StatusFull, StatusUnknown:
		return true
	}
	return false
}

type Place struct {
	ID          uuid.UUID
	Name        string
	Category    Category
	Latitude    float64
	Longitude   float64
	Address     *string
	Status      Status
	Description *string
	Contact     *string
	Source      *string
	// CreatedByDevice is the anonymous device that added the place; nil for
	// operator-curated places. Never exposed by the API.
	CreatedByDevice *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Origin string

const (
	OriginOfficial  Origin = "official"
	OriginCommunity Origin = "community"
)

func (p Place) Origin() Origin {
	if p.CreatedByDevice != nil {
		return OriginCommunity
	}
	return OriginOfficial
}

// OwnedBy reports whether deviceID added this place.
func (p Place) OwnedBy(deviceID string) bool {
	return deviceID != "" && p.CreatedByDevice != nil && *p.CreatedByDevice == deviceID
}

func ValidateDeviceID(deviceID string) error {
	if len(deviceID) < 8 || len(deviceID) > 128 {
		return apperr.Validation("device_id is invalid", map[string]string{"device_id": "must be between 8 and 128 characters"})
	}
	return nil
}

// Fields is the editable part of a place; nil means "unchanged" on update.
type Fields struct {
	Name        *string
	Category    *Category
	Latitude    *float64
	Longitude   *float64
	Address     *string
	Status      *Status
	Description *string
	Contact     *string
	Source      *string
}

func tooLong(s *string, max int) bool {
	return s != nil && len([]rune(strings.TrimSpace(*s))) > max
}

func (f Fields) Validate(requireAll bool) error {
	fields := map[string]string{}
	if f.Name != nil {
		if n := len([]rune(strings.TrimSpace(*f.Name))); n < 1 || n > 120 {
			fields["name"] = "must be 1-120 characters"
		}
	} else if requireAll {
		fields["name"] = "is required"
	}
	if f.Category != nil {
		if !f.Category.Valid() {
			fields["category"] = "must be one of hospital, shelter, food_water, rescue, police, fuel, vehicle_repair, boat_point, other"
		}
	} else if requireAll {
		fields["category"] = "is required"
	}
	if f.Status != nil && !f.Status.Valid() {
		fields["status"] = "must be one of open, closed, full, unknown"
	}
	if (f.Latitude == nil) != (f.Longitude == nil) {
		fields["latitude"] = "latitude and longitude must be sent together"
	} else if f.Latitude != nil && (*f.Latitude < -90 || *f.Latitude > 90 || *f.Longitude < -180 || *f.Longitude > 180) {
		fields["latitude"] = "must be a valid latitude/longitude"
	} else if f.Latitude == nil && requireAll {
		fields["latitude"] = "is required"
	}
	if tooLong(f.Address, 300) {
		fields["address"] = "must be 300 characters or fewer"
	}
	if tooLong(f.Description, 2000) {
		fields["description"] = "must be 2000 characters or fewer"
	}
	if tooLong(f.Contact, 120) {
		fields["contact"] = "must be 120 characters or fewer"
	}
	if tooLong(f.Source, 200) {
		fields["source"] = "must be 200 characters or fewer"
	}
	if len(fields) > 0 {
		return apperr.Validation("important place is invalid", fields)
	}
	return nil
}

// optional trims s; blank clears the field.
func optional(s *string) *string {
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

// Apply writes the present fields onto p.
func (f Fields) Apply(p *Place) {
	if f.Name != nil {
		p.Name = strings.TrimSpace(*f.Name)
	}
	if f.Category != nil {
		p.Category = *f.Category
	}
	if f.Latitude != nil && f.Longitude != nil {
		p.Latitude, p.Longitude = *f.Latitude, *f.Longitude
	}
	if f.Status != nil {
		p.Status = *f.Status
	}
	if f.Address != nil {
		p.Address = optional(f.Address)
	}
	if f.Description != nil {
		p.Description = optional(f.Description)
	}
	if f.Contact != nil {
		p.Contact = optional(f.Contact)
	}
	if f.Source != nil {
		p.Source = optional(f.Source)
	}
}
