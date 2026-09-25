// Package route holds the deterministic route-risk rules: given candidate
// routes from an external routing provider and the open community reports
// near them, how risky is each route for one vehicle class. No routing
// engine lives here and nothing is predicted — every outcome follows from a
// report's passability, severity, freshness and distance from the route.
package route

import (
	"math"
	"sort"
	"time"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/report"
)

// Vehicle is the road-user class a route is evaluated for. Values match the
// report passability keys.
type Vehicle string

const (
	VehicleWalk       Vehicle = "walk"
	VehicleMotorcycle Vehicle = "motorcycle"
	VehicleSedan      Vehicle = "sedan"
	VehicleSUVPickup  Vehicle = "suv_pickup"
)

func (v Vehicle) Valid() bool {
	switch v {
	case VehicleWalk, VehicleMotorcycle, VehicleSedan, VehicleSUVPickup:
		return true
	}
	return false
}

// Profile is the routing-provider profile used for a vehicle: pedestrians
// get footpaths, every motor vehicle uses the road network.
type Profile string

const (
	ProfileFoot    Profile = "foot"
	ProfileDriving Profile = "driving"
)

func (v Vehicle) Profile() Profile {
	if v == VehicleWalk {
		return ProfileFoot
	}
	return ProfileDriving
}

// Risk is a route's overall verdict. "safe" only ever means "no open
// community report affects this route" — never a guarantee.
type Risk string

const (
	RiskSafe    Risk = "safe"
	RiskCaution Risk = "caution"
	RiskBlocked Risk = "blocked"
)

var riskRank = map[Risk]int{RiskSafe: 0, RiskCaution: 1, RiskBlocked: 2}

// Impact is what one report means for the route.
type Impact string

const (
	ImpactBlocked Impact = "blocked"
	ImpactCaution Impact = "caution"
	ImpactInfo    Impact = "info"
)

var impactRank = map[Impact]int{ImpactInfo: 0, ImpactCaution: 1, ImpactBlocked: 2}

func (i Impact) risk() Risk {
	switch i {
	case ImpactBlocked:
		return RiskBlocked
	case ImpactCaution:
		return RiskCaution
	}
	return RiskSafe
}

// Reason explains an impact in a way the client can translate.
type Reason string

const (
	ReasonImpassable     Reason = "impassable_for_vehicle"
	ReasonNotRecommended Reason = "not_recommended_for_vehicle"
	ReasonCaution        Reason = "caution_for_vehicle"
	ReasonStale          Reason = "possibly_outdated"     // a blocking report nobody re-confirmed
	ReasonSevereUnknown  Reason = "severe_unknown_access" // severe road incident, passability unknown
	ReasonRoadClosed     Reason = "road_closed"
	ReasonPassable       Reason = "passable_for_vehicle"
	ReasonNearby         Reason = "nearby_incident" // not a road condition (e.g. power outage)
)

// Point is a WGS84 coordinate.
type Point struct {
	Latitude, Longitude float64
}

// Candidate is one route returned by the routing provider.
type Candidate struct {
	DistanceM float64
	DurationS float64
	Path      []Point
}

// AffectedIncident is a report close enough to a route to matter.
type AffectedIncident struct {
	Report             report.ReportWithStats
	DistanceFromRouteM float64
	Impact             Impact
	Reason             Reason
}

// Evaluation is a candidate plus its verdict.
type Evaluation struct {
	Candidate
	Risk       Risk
	Incidents  []AffectedIncident // most important first
	Blocking   int
	Cautioning int
}

// MaxTripDistanceM bounds a request so a single evaluation can't scan a
// whole country's reports.
const MaxTripDistanceM = 300_000.0

// ValidateTrip checks origin/destination/vehicle.
func ValidateTrip(origin, destination Point, vehicle Vehicle) error {
	fields := map[string]string{}
	for name, p := range map[string]Point{"origin": origin, "destination": destination} {
		if p.Latitude < -90 || p.Latitude > 90 || p.Longitude < -180 || p.Longitude > 180 {
			fields[name] = "must be a valid latitude/longitude"
		}
	}
	if !vehicle.Valid() {
		fields["vehicle"] = "must be one of walk, motorcycle, sedan, suv_pickup"
	}
	if len(fields) == 0 {
		d := HaversineM(origin, destination)
		switch {
		case d < 20:
			fields["destination"] = "must be at least 20 m from the origin"
		case d > MaxTripDistanceM:
			fields["destination"] = "must be within 300 km of the origin"
		}
	}
	if len(fields) > 0 {
		return apperr.Validation("route request is invalid", fields)
	}
	return nil
}

// AffectRadiusM is how close a report must be to the route line to count.
// Floods spread along a road, so they get a wider corridor than point
// incidents such as a stalled car.
func AffectRadiusM(t report.Type) float64 {
	if t == report.TypeFlooded {
		return 100
	}
	return 50
}

// MaxAffectRadiusM is the widest corridor any category uses; the repository
// query pads route bounding boxes by it.
const MaxAffectRadiusM = 100.0

// DepthPassability is the conservative fallback used when a flood report
// has a depth but no per-vehicle passability. It mirrors the form's
// pre-fill (apps/web/lib/report-meta.ts suggestPassability).
func DepthPassability(d report.WaterDepth) (report.Passability, bool) {
	switch d {
	case report.WaterDepthAnkle:
		return report.Passability{Walk: report.PassPassable, Motorcycle: report.PassPassable, Sedan: report.PassPassable, SUVPickup: report.PassPassable}, true
	case report.WaterDepthShin:
		return report.Passability{Walk: report.PassCaution, Motorcycle: report.PassCaution, Sedan: report.PassCaution, SUVPickup: report.PassPassable}, true
	case report.WaterDepthKnee:
		return report.Passability{Walk: report.PassNotRecommended, Motorcycle: report.PassImpassable, Sedan: report.PassNotRecommended, SUVPickup: report.PassCaution}, true
	case report.WaterDepthAboveKnee:
		return report.Passability{Walk: report.PassImpassable, Motorcycle: report.PassImpassable, Sedan: report.PassImpassable, SUVPickup: report.PassNotRecommended}, true
	}
	return report.Passability{}, false
}

func levelFor(p report.Passability, v Vehicle) report.PassLevel {
	switch v {
	case VehicleWalk:
		return p.Walk
	case VehicleMotorcycle:
		return p.Motorcycle
	case VehicleSedan:
		return p.Sedan
	case VehicleSUVPickup:
		return p.SUVPickup
	}
	return report.PassUnknown
}

// PassLevelFor is the report's passability for v: the reporter's own
// assessment, else the flood-depth fallback, else unknown.
func PassLevelFor(r report.Report, v Vehicle) report.PassLevel {
	if r.Passability != nil {
		if l := levelFor(*r.Passability, v); l != report.PassUnknown {
			return l
		}
	}
	if r.Type == report.TypeFlooded && r.WaterDepth != nil {
		if p, ok := DepthPassability(*r.WaterDepth); ok {
			return levelFor(p, v)
		}
	}
	return report.PassUnknown
}

// ImpactOf classifies one open report for a vehicle. The table:
//
//	non-road category (power outage, help, shelter…) → info
//	impassable        → blocked (caution if the report is possibly stale)
//	not_recommended   → caution
//	caution           → caution
//	passable          → info
//	unknown           → caution for road_closed or high/critical severity, else info
func ImpactOf(r report.Report, v Vehicle, now time.Time) (Impact, Reason) {
	if !r.Type.AffectsRoad() {
		return ImpactInfo, ReasonNearby
	}
	stale := r.Status(now) == report.StatusPossiblyStale
	switch PassLevelFor(r, v) {
	case report.PassImpassable:
		if stale {
			return ImpactCaution, ReasonStale
		}
		return ImpactBlocked, ReasonImpassable
	case report.PassNotRecommended:
		return ImpactCaution, ReasonNotRecommended
	case report.PassCaution:
		return ImpactCaution, ReasonCaution
	case report.PassPassable:
		return ImpactInfo, ReasonPassable
	}
	if r.Type == report.TypeRoadClosed {
		return ImpactCaution, ReasonRoadClosed
	}
	if r.Severity.IsSevere() {
		return ImpactCaution, ReasonSevereUnknown
	}
	return ImpactInfo, ReasonNearby
}

// Evaluate scores every candidate against the reports (which must already be
// open and visible). Evaluations are returned safest first, then fastest.
func Evaluate(candidates []Candidate, reports []report.ReportWithStats, v Vehicle, now time.Time) []Evaluation {
	out := make([]Evaluation, 0, len(candidates))
	for _, c := range candidates {
		ev := Evaluation{Candidate: c, Risk: RiskSafe}
		for _, r := range reports {
			if !r.Status(now).IsOpen() || r.HiddenAt != nil {
				continue
			}
			d := DistanceToPathM(Point{r.Latitude, r.Longitude}, c.Path)
			if d > AffectRadiusM(r.Type) {
				continue
			}
			impact, reason := ImpactOf(r.Report, v, now)
			ev.Incidents = append(ev.Incidents, AffectedIncident{Report: r, DistanceFromRouteM: d, Impact: impact, Reason: reason})
			switch impact {
			case ImpactBlocked:
				ev.Blocking++
			case ImpactCaution:
				ev.Cautioning++
			}
			if riskRank[impact.risk()] > riskRank[ev.Risk] {
				ev.Risk = impact.risk()
			}
		}
		sort.SliceStable(ev.Incidents, func(i, j int) bool {
			a, b := ev.Incidents[i], ev.Incidents[j]
			if impactRank[a.Impact] != impactRank[b.Impact] {
				return impactRank[a.Impact] > impactRank[b.Impact]
			}
			if a.Report.Severity.Rank() != b.Report.Severity.Rank() {
				return a.Report.Severity.Rank() > b.Report.Severity.Rank()
			}
			return a.DistanceFromRouteM < b.DistanceFromRouteM
		})
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if riskRank[out[i].Risk] != riskRank[out[j].Risk] {
			return riskRank[out[i].Risk] < riskRank[out[j].Risk]
		}
		return out[i].DurationS < out[j].DurationS
	})
	return out
}

const earthRadiusM = 6_371_000.0

// HaversineM is the great-circle distance between two points in meters.
func HaversineM(a, b Point) float64 {
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(b.Latitude - a.Latitude)
	dLng := toRad(b.Longitude - a.Longitude)
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(toRad(a.Latitude))*math.Cos(toRad(b.Latitude))*math.Pow(math.Sin(dLng/2), 2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(h))
}

// DistanceToPathM is the shortest distance from p to the polyline. Each
// segment is projected onto a local equirectangular plane around p, which is
// accurate to well under a meter at the tens-of-meters scale that matters.
func DistanceToPathM(p Point, path []Point) float64 {
	if len(path) == 0 {
		return math.Inf(1)
	}
	if len(path) == 1 {
		return HaversineM(p, path[0])
	}
	cosLat := math.Cos(p.Latitude * math.Pi / 180)
	toXY := func(q Point) (float64, float64) {
		return (q.Longitude - p.Longitude) * math.Pi / 180 * earthRadiusM * cosLat, (q.Latitude - p.Latitude) * math.Pi / 180 * earthRadiusM
	}
	best := math.Inf(1)
	ax, ay := toXY(path[0])
	for _, q := range path[1:] {
		bx, by := toXY(q)
		dx, dy := bx-ax, by-ay
		t := 0.0
		if l2 := dx*dx + dy*dy; l2 > 0 {
			t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/l2))
		}
		cx, cy := ax+t*dx, ay+t*dy
		if d := math.Hypot(cx, cy); d < best {
			best = d
		}
		ax, ay = bx, by
	}
	return best
}

// BBox is a lat/lng rectangle.
type BBox struct {
	MinLat, MaxLat, MinLng, MaxLng float64
}

// CorridorBoxes splits a path into at most maxBoxes consecutive chunks and
// returns each chunk's bounding box padded by padM meters. Querying these
// boxes (instead of the route's single, mostly empty bounding box) keeps the
// report lookup proportional to the corridor, not the trip's extent.
func CorridorBoxes(path []Point, padM float64, maxBoxes int) []BBox {
	if len(path) == 0 || maxBoxes < 1 {
		return nil
	}
	chunk := int(math.Ceil(float64(len(path)) / float64(maxBoxes)))
	if chunk < 1 {
		chunk = 1
	}
	var out []BBox
	for start := 0; start < len(path); start += chunk {
		end := start + chunk + 1 // overlap one point so segments crossing chunks are covered
		if end > len(path) {
			end = len(path)
		}
		b := BBox{MinLat: 90, MaxLat: -90, MinLng: 180, MaxLng: -180}
		for _, q := range path[start:end] {
			b.MinLat = math.Min(b.MinLat, q.Latitude)
			b.MaxLat = math.Max(b.MaxLat, q.Latitude)
			b.MinLng = math.Min(b.MinLng, q.Longitude)
			b.MaxLng = math.Max(b.MaxLng, q.Longitude)
		}
		dLat := padM / 111_320
		dLng := padM / (111_320 * math.Max(math.Cos(b.MinLat*math.Pi/180), 0.01))
		out = append(out, BBox{
			MinLat: math.Max(-90, b.MinLat-dLat), MaxLat: math.Min(90, b.MaxLat+dLat),
			MinLng: math.Max(-180, b.MinLng-dLng), MaxLng: math.Min(180, b.MaxLng+dLng),
		})
	}
	return out
}
