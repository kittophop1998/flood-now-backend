package route

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/report"
)

var now = time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// A straight west→east route along latitude 13.75.
var straight = Candidate{DistanceM: 2000, DurationS: 300, Path: []Point{{13.75, 100.50}, {13.75, 100.52}}}

func incident(t report.Type, lat, lng float64, mutate func(*report.Report)) report.ReportWithStats {
	r := report.Report{
		ID: uuid.New(), Type: t, Severity: report.SeverityModerate, Latitude: lat, Longitude: lng,
		CreatedAt: now.Add(-10 * time.Minute), LastVerifiedAt: now.Add(-10 * time.Minute),
		StaleAt: now.Add(time.Hour), ExpiresAt: now.Add(5 * time.Hour),
	}
	if mutate != nil {
		mutate(&r)
	}
	return report.ReportWithStats{Report: r}
}

func pass(level report.PassLevel) *report.Passability {
	return &report.Passability{Walk: report.PassPassable, Motorcycle: report.PassPassable, Sedan: level, SUVPickup: report.PassPassable}
}

func TestSedanImpassableBlocksRoute(t *testing.T) {
	blocked := incident(report.TypeFlooded, 13.7502, 100.51, func(r *report.Report) { r.Passability = pass(report.PassImpassable) })

	sedan := Evaluate([]Candidate{straight}, []report.ReportWithStats{blocked}, VehicleSedan, now)[0]
	if sedan.Risk != RiskBlocked || sedan.Blocking != 1 || sedan.Incidents[0].Reason != ReasonImpassable {
		t.Fatalf("sedan: risk=%s blocking=%d", sedan.Risk, sedan.Blocking)
	}
	// Same report, but the SUV was reported passable there.
	suv := Evaluate([]Candidate{straight}, []report.ReportWithStats{blocked}, VehicleSUVPickup, now)[0]
	if suv.Risk != RiskSafe || suv.Incidents[0].Impact != ImpactInfo {
		t.Fatalf("suv: risk=%s", suv.Risk)
	}
}

func TestStaleBlockingReportOnlyCautions(t *testing.T) {
	stale := incident(report.TypeRoadClosed, 13.7501, 100.51, func(r *report.Report) {
		r.Passability = pass(report.PassImpassable)
		r.StaleAt = now.Add(-time.Minute)
	})
	ev := Evaluate([]Candidate{straight}, []report.ReportWithStats{stale}, VehicleSedan, now)[0]
	if ev.Risk != RiskCaution || ev.Incidents[0].Reason != ReasonStale {
		t.Fatalf("risk=%s reason=%s", ev.Risk, ev.Incidents[0].Reason)
	}
}

func TestDistanceFromRouteDecidesRelevance(t *testing.T) {
	// ~80 m off the route: inside a flood's 100 m corridor, outside the 50 m
	// corridor of a stalled vehicle.
	offset := 80.0 / 111_320
	flood := incident(report.TypeFlooded, 13.75+offset, 100.51, func(r *report.Report) { r.Passability = pass(report.PassImpassable) })
	stalled := incident(report.TypeVehicleStalled, 13.75+offset, 100.51, func(r *report.Report) { r.Passability = pass(report.PassImpassable) })

	if ev := Evaluate([]Candidate{straight}, []report.ReportWithStats{flood}, VehicleSedan, now)[0]; ev.Risk != RiskBlocked {
		t.Errorf("flood 80 m away should affect the route, got %s", ev.Risk)
	}
	if ev := Evaluate([]Candidate{straight}, []report.ReportWithStats{stalled}, VehicleSedan, now)[0]; ev.Risk != RiskSafe || len(ev.Incidents) != 0 {
		t.Errorf("stalled car 80 m away should not affect the route, got %s", ev.Risk)
	}
}

func TestUnknownPassabilityRules(t *testing.T) {
	cases := []struct {
		name string
		r    report.ReportWithStats
		want Impact
	}{
		{"flood depth fallback: knee blocks a motorcycle", incident(report.TypeFlooded, 13.75, 100.51, func(r *report.Report) { r.WaterDepth = ptr(report.WaterDepthKnee) }), ImpactBlocked},
		{"road closed without passability cautions", incident(report.TypeRoadClosed, 13.75, 100.51, nil), ImpactCaution},
		{"severe accident without passability cautions", incident(report.TypeAccident, 13.75, 100.51, func(r *report.Report) { r.Severity = report.SeverityCritical }), ImpactCaution},
		{"minor obstruction without passability is info", incident(report.TypeObstruction, 13.75, 100.51, nil), ImpactInfo},
		{"power outage never affects risk", incident(report.TypePowerOutage, 13.75, 100.51, func(r *report.Report) { r.Severity = report.SeverityCritical }), ImpactInfo},
	}
	for _, c := range cases {
		got, _ := ImpactOf(c.r.Report, VehicleMotorcycle, now)
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestClosedAndHiddenReportsAreIgnoredAndRoutesSortSafestFirst(t *testing.T) {
	resolved := incident(report.TypeFlooded, 13.75, 100.51, func(r *report.Report) {
		r.Passability = pass(report.PassImpassable)
		r.ResolvedAt = ptr(now)
	})
	hidden := incident(report.TypeFlooded, 13.75, 100.51, func(r *report.Report) {
		r.Passability = pass(report.PassImpassable)
		r.HiddenAt = ptr(now)
	})
	blocker := incident(report.TypeFlooded, 13.75, 100.51, func(r *report.Report) { r.Passability = pass(report.PassImpassable) })

	detour := Candidate{DistanceM: 3000, DurationS: 600, Path: []Point{{13.76, 100.50}, {13.76, 100.52}}}
	evs := Evaluate([]Candidate{straight, detour}, []report.ReportWithStats{resolved, hidden, blocker}, VehicleSedan, now)
	if evs[0].DurationS != 600 || evs[0].Risk != RiskSafe || evs[1].Risk != RiskBlocked {
		t.Fatalf("expected the safe detour first: %+v / %+v", evs[0].Risk, evs[1].Risk)
	}
	if len(evs[1].Incidents) != 1 {
		t.Errorf("resolved/hidden reports must not count, got %d incidents", len(evs[1].Incidents))
	}
}

func TestDistanceToPath(t *testing.T) {
	d := DistanceToPathM(Point{13.751, 100.51}, straight.Path)
	if math.Abs(d-111.3) > 1 {
		t.Errorf("distance to segment interior = %.1f m, want ~111 m", d)
	}
	// Beyond the segment end: distance to the endpoint.
	d = DistanceToPathM(Point{13.75, 100.53}, straight.Path)
	if want := HaversineM(Point{13.75, 100.53}, Point{13.75, 100.52}); math.Abs(d-want) > 1 {
		t.Errorf("distance past the end = %.1f, want %.1f", d, want)
	}
}

func TestCorridorBoxesCoverThePath(t *testing.T) {
	path := make([]Point, 101)
	for i := range path {
		path[i] = Point{13.7 + float64(i)*0.001, 100.5 + float64(i)*0.001}
	}
	boxes := CorridorBoxes(path, 100, 8)
	if len(boxes) == 0 || len(boxes) > 8 {
		t.Fatalf("got %d boxes", len(boxes))
	}
	for _, p := range path {
		covered := false
		for _, b := range boxes {
			covered = covered || (p.Latitude >= b.MinLat && p.Latitude <= b.MaxLat && p.Longitude >= b.MinLng && p.Longitude <= b.MaxLng)
		}
		if !covered {
			t.Fatalf("point %v not covered", p)
		}
	}
}

func TestValidateTrip(t *testing.T) {
	a, b := Point{13.75, 100.5}, Point{13.76, 100.51}
	if err := ValidateTrip(a, b, VehicleSedan); err != nil {
		t.Errorf("valid trip rejected: %v", err)
	}
	for name, err := range map[string]error{
		"unknown vehicle": ValidateTrip(a, b, "tank"),
		"same point":      ValidateTrip(a, a, VehicleWalk),
		"too far":         ValidateTrip(a, Point{18.8, 98.98}, VehicleSedan),
		"bad latitude":    ValidateTrip(Point{95, 100}, b, VehicleSedan),
	} {
		if err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	if VehicleWalk.Profile() != ProfileFoot || VehicleMotorcycle.Profile() != ProfileDriving {
		t.Error("vehicle → routing profile mapping changed")
	}
}
