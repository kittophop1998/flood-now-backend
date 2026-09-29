// Package localservice is the commercial side of FloodNow: local service
// providers (towing, repair shops, electricians…), customer service
// requests, provider offers, the qualified match and the provider credit
// wallet that pays for it.
//
// It is deliberately separate from domain/sos. SOS and volunteer helpers are
// free community help and are never billed, ranked or offered here; a
// provider profile never grants or implies helper mode, and vice versa.
package localservice

import (
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/upload"
)

// Category is a kind of commercial service.
type Category string

const (
	CatTowing         Category = "towing"
	CatAutoRepair     Category = "auto_repair"
	CatTyre           Category = "tyre"
	CatBattery        Category = "battery"
	CatMobileMechanic Category = "mobile_mechanic"
	CatTransport      Category = "transport"
	CatElectrician    Category = "electrician"
	CatPlumber        Category = "plumber"
	CatWaterPump      Category = "water_pump"
	CatOther          Category = "other"
)

var AllCategories = []Category{CatTowing, CatAutoRepair, CatTyre, CatBattery, CatMobileMechanic, CatTransport, CatElectrician, CatPlumber, CatWaterPump, CatOther}

func (c Category) Valid() bool { return slices.Contains(AllCategories, c) }

// vehicleCategories take the optional vehicle description on a request.
var vehicleCategories = []Category{CatTowing, CatAutoRepair, CatTyre, CatBattery, CatMobileMechanic}

// TakesVehicleInfo reports whether a request of this category may carry
// vehicle details (make/model/plate…); others drop them.
func (c Category) TakesVehicleInfo() bool { return slices.Contains(vehicleCategories, c) }

// ProviderStatus is operator-controlled: a suspended provider is hidden and
// can't act.
type ProviderStatus string

const (
	ProviderActive    ProviderStatus = "active"
	ProviderSuspended ProviderStatus = "suspended"
)

func (s ProviderStatus) Valid() bool { return s == ProviderActive || s == ProviderSuspended }

// AllowedServiceRadiiM are the distances a provider can cover from their base.
var AllowedServiceRadiiM = []int{3000, 5000, 10000, 20000, 50000}

// Provider is a user's service-provider (shop/business) profile.
type Provider struct {
	ID               uuid.UUID
	OwnerUserID      uuid.UUID
	DisplayName      string
	Categories       []Category
	Description      *string
	Phone            string
	LineID           *string
	Latitude         float64
	Longitude        float64
	LocationName     *string
	ServiceRadiusM   int
	BusinessHours    *string
	MobileService    bool
	Available        bool
	StartingPriceTHB *int
	LogoKey          *string
	// VerifiedAt is set only by an operator after a real review. Paying for
	// credit or anything else never sets it.
	VerifiedAt *time.Time
	Status     ProviderStatus
	// CreditBalance is the cached ledger sum (see wallet.go).
	CreditBalance int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (p Provider) Verified() bool { return p.VerifiedAt != nil }

// CanWork reports whether the provider may receive requests, send offers
// and accept matches right now.
func (p Provider) CanWork() bool { return p.Status == ProviderActive && p.Available }

func (p Provider) HasCategory(c Category) bool { return slices.Contains(p.Categories, c) }

// ProviderInput is a full profile create/replace from the owner.
type ProviderInput struct {
	DisplayName      string
	Categories       []Category
	Description      *string
	Phone            string
	LineID           *string
	Latitude         float64
	Longitude        float64
	LocationName     *string
	ServiceRadiusM   int
	BusinessHours    *string
	MobileService    bool
	Available        bool
	StartingPriceTHB *int
	LogoKey          *string
}

func (in ProviderInput) Validate() error {
	fields := map[string]string{}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.DisplayName)); n < 1 || n > 80 {
		fields["display_name"] = "must be 1-80 characters"
	}
	if len(in.Categories) == 0 || len(in.Categories) > len(AllCategories) {
		fields["categories"] = "choose at least one service category"
	}
	seen := map[Category]bool{}
	for _, c := range in.Categories {
		if !c.Valid() {
			fields["categories"] = "contains an unknown category: " + string(c)
		} else if seen[c] {
			fields["categories"] = "must not repeat a category"
		}
		seen[c] = true
	}
	if in.Description != nil && utf8.RuneCountInString(*in.Description) > 2000 {
		fields["description"] = "must be 2000 characters or fewer"
	}
	if p := strings.TrimSpace(in.Phone); len(p) < 3 || len(p) > 32 {
		fields["phone"] = "must be 3-32 characters"
	}
	if in.LineID != nil && utf8.RuneCountInString(strings.TrimSpace(*in.LineID)) > 64 {
		fields["line_id"] = "must be 64 characters or fewer"
	}
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		fields["latitude"] = "must be a valid latitude/longitude"
	}
	if in.LocationName != nil && utf8.RuneCountInString(*in.LocationName) > 200 {
		fields["location_name"] = "must be 200 characters or fewer"
	}
	if !slices.Contains(AllowedServiceRadiiM, in.ServiceRadiusM) {
		fields["service_radius_m"] = "must be one of 3000, 5000, 10000, 20000, 50000"
	}
	if in.BusinessHours != nil && utf8.RuneCountInString(*in.BusinessHours) > 200 {
		fields["business_hours"] = "must be 200 characters or fewer"
	}
	if in.StartingPriceTHB != nil && (*in.StartingPriceTHB < 0 || *in.StartingPriceTHB > 1_000_000) {
		fields["starting_price_thb"] = "must be between 0 and 1000000"
	}
	if in.LogoKey != nil && *in.LogoKey != "" && !ValidImageKey(*in.LogoKey) {
		fields["logo_key"] = "must be a key returned by /uploads/presign"
	}
	if len(fields) > 0 {
		return apperr.Validation("provider profile is invalid", fields)
	}
	return nil
}

// Apply copies the (validated) input onto p, trimming optional text.
func (in ProviderInput) Apply(p *Provider) {
	p.DisplayName = strings.TrimSpace(in.DisplayName)
	p.Categories = slices.Clone(in.Categories)
	p.Description = Trimmed(in.Description)
	p.Phone = strings.TrimSpace(in.Phone)
	p.LineID = Trimmed(in.LineID)
	p.Latitude, p.Longitude = in.Latitude, in.Longitude
	p.LocationName = Trimmed(in.LocationName)
	p.ServiceRadiusM = in.ServiceRadiusM
	p.BusinessHours = Trimmed(in.BusinessHours)
	p.MobileService = in.MobileService
	p.Available = in.Available
	p.StartingPriceTHB = in.StartingPriceTHB
	p.LogoKey = Trimmed(in.LogoKey)
}

// ValidImageKey accepts keys minted by the public presign (reports/…),
// the same rule community events use.
func ValidImageKey(key string) bool {
	return strings.HasPrefix(key, upload.ReportKeyPrefix) && !strings.Contains(key, "..") && !strings.Contains(key, "://") && len(key) <= 300
}

// ApproximateCoordinate rounds to 3 decimals (~110 m): what anyone who isn't
// a matched party sees of a request, and what the public sees of a provider
// base.
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

// DistanceM is the great-circle distance between two points in meters (the
// same haversine the SQL queries use).
func DistanceM(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
