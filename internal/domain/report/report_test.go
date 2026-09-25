package report

import (
	"testing"
	"time"
)

func TestNewReportInputValidate(t *testing.T) {
	valid := func() NewReportInput {
		return NewReportInput{
			Type:      TypeFlooded,
			Severity:  SeverityCritical,
			Latitude:  13.75,
			Longitude: 100.5,
		}
	}

	t.Run("valid input passes", func(t *testing.T) {
		if err := valid().Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("every creatable category is valid", func(t *testing.T) {
		for _, ty := range creatableTypes {
			in := valid()
			in.Type = Type(ty)
			if err := in.Validate(); err != nil {
				t.Errorf("type %s: unexpected error %v", ty, err)
			}
		}
	})

	cases := map[string]func(*NewReportInput){
		"invalid type":             func(in *NewReportInput) { in.Type = "not-a-type" },
		"legacy road_blocked":      func(in *NewReportInput) { in.Type = "road_blocked" },
		"sos-only vehicle_stalled": func(in *NewReportInput) { in.Type = TypeVehicleStalled },
		"sos-only help_needed":     func(in *NewReportInput) { in.Type = TypeHelpNeeded },
		"sos-only other":           func(in *NewReportInput) { in.Type = TypeOther },
		"invalid severity":         func(in *NewReportInput) { in.Severity = "impassable" },
		"out of range latitude":    func(in *NewReportInput) { in.Latitude = 91 },
		"out of range longitude":   func(in *NewReportInput) { in.Longitude = -181 },
		"non-point geometry":       func(in *NewReportInput) { in.GeometryType = GeometryArea },
		"negative water level":     func(in *NewReportInput) { v := -5; in.WaterLevelCM = &v },
		"invalid water depth":      func(in *NewReportInput) { d := WaterDepth("waist"); in.WaterDepth = &d },
		"invalid passability": func(in *NewReportInput) {
			in.Passability = &Passability{Walk: PassPassable, Motorcycle: "fly", Sedan: PassUnknown, SUVPickup: PassUnknown}
		},
		"path-traversal image key": func(in *NewReportInput) { k := "../../etc/passwd"; in.ImageKey = &k },
	}
	for name, mutate := range cases {
		t.Run(name+" rejected", func(t *testing.T) {
			in := valid()
			mutate(&in)
			if err := in.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	t.Run("full flood report accepted", func(t *testing.T) {
		in := valid()
		d := WaterDepthKnee
		in.WaterDepth = &d
		in.Passability = &Passability{Walk: PassCaution, Motorcycle: PassImpassable, Sedan: PassImpassable, SUVPickup: PassCaution}
		key := "reports/2026/09/25/abc.jpg"
		in.ImageKey = &key
		if err := in.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})
}

func TestNormalizedDropsFieldsThatDontApply(t *testing.T) {
	d := WaterDepthKnee
	cm := 40
	pass := &Passability{Walk: PassPassable, Motorcycle: PassPassable, Sedan: PassPassable, SUVPickup: PassPassable}

	shelter := NewReportInput{Type: TypeShelter, WaterDepth: &d, WaterLevelCM: &cm, Passability: pass}.Normalized()
	if shelter.WaterDepth != nil || shelter.WaterLevelCM != nil || shelter.Passability != nil {
		t.Errorf("shelter kept flood/road fields: %+v", shelter)
	}
	if shelter.GeometryType != GeometryPoint {
		t.Errorf("geometry_type = %q, want point", shelter.GeometryType)
	}

	accident := NewReportInput{Type: TypeAccident, WaterDepth: &d, Passability: pass}.Normalized()
	if accident.WaterDepth != nil {
		t.Error("accident kept water depth")
	}
	if accident.Passability == nil {
		t.Error("accident lost passability; it's a road category")
	}

	flood := NewReportInput{Type: TypeFlooded, WaterDepth: &d, Passability: pass}.Normalized()
	if flood.WaterDepth == nil || flood.Passability == nil {
		t.Error("flood lost its depth/passability")
	}
}

func TestReportStatus(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	base := Report{StaleAt: now.Add(time.Hour), ExpiresAt: now.Add(3 * time.Hour)}

	if got := base.Status(now); got != StatusActive {
		t.Errorf("fresh report: got %s, want active", got)
	}
	if got := base.Status(now.Add(time.Hour)); got != StatusPossiblyStale {
		t.Errorf("at stale_at: got %s, want possibly_stale", got)
	}
	if got := base.Status(now.Add(3 * time.Hour)); got != StatusExpired {
		t.Errorf("at expires_at: got %s, want expired", got)
	}

	resolved := base
	at := now.Add(-time.Minute)
	resolved.ResolvedAt = &at
	if got := resolved.Status(now.Add(5 * time.Hour)); got != StatusResolved {
		t.Errorf("resolved report: got %s, want resolved (resolution wins over expiry)", got)
	}
}

func testPolicy() FreshnessPolicy {
	return FreshnessPolicy{
		StaleAfter:         2 * time.Hour,
		TTL:                6 * time.Hour,
		FacilityStaleAfter: 12 * time.Hour,
		FacilityTTL:        48 * time.Hour,
		ResolveThreshold:   2,
	}
}

func TestFreshnessPolicyWindow(t *testing.T) {
	p := testPolicy()
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)

	stale, exp := p.Window(TypeFlooded, now)
	if !stale.Equal(now.Add(2*time.Hour)) || !exp.Equal(now.Add(6*time.Hour)) {
		t.Errorf("flood window = %v / %v", stale, exp)
	}
	stale, exp = p.Window(TypeShelter, now)
	if !stale.Equal(now.Add(12*time.Hour)) || !exp.Equal(now.Add(48*time.Hour)) {
		t.Errorf("shelter window = %v / %v", stale, exp)
	}
}

func TestFreshnessPolicyValidate(t *testing.T) {
	if err := testPolicy().Validate(); err != nil {
		t.Fatalf("default policy invalid: %v", err)
	}
	p := testPolicy()
	p.StaleAfter = 7 * time.Hour
	if err := p.Validate(); err == nil {
		t.Error("stale-after > TTL should be rejected")
	}
	p = testPolicy()
	p.ResolveThreshold = 0
	if err := p.Validate(); err == nil {
		t.Error("zero resolve threshold should be rejected")
	}
}

func TestNextResolvedAt(t *testing.T) {
	p := testPolicy()
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)

	if got := p.NextResolvedAt(nil, 0, 1, now); got != nil {
		t.Error("a single cleared vote must not resolve (threshold 2)")
	}
	if got := p.NextResolvedAt(nil, 2, 2, now); got != nil {
		t.Error("cleared must outnumber still-active votes")
	}
	if got := p.NextResolvedAt(nil, 1, 2, now); got == nil || !got.Equal(now) {
		t.Errorf("expected resolution at now, got %v", got)
	}
	if got := p.NextResolvedAt(&earlier, 1, 3, now); got == nil || !got.Equal(earlier) {
		t.Errorf("already-resolved report should keep its resolved_at, got %v", got)
	}
	if got := p.NextResolvedAt(&earlier, 3, 2, now); got != nil {
		t.Error("report should re-open once still-active votes catch up")
	}
}

func TestResolutionEvent(t *testing.T) {
	at := time.Now()
	if ResolutionEvent(nil, &at) != EventResolved {
		t.Error("nil -> set should be resolved")
	}
	if ResolutionEvent(&at, nil) != EventReopened {
		t.Error("set -> nil should be reopened")
	}
	if ResolutionEvent(nil, nil) != "" || ResolutionEvent(&at, &at) != "" {
		t.Error("unchanged resolution should emit nothing")
	}
}

func TestCategoryRules(t *testing.T) {
	if !TypeFlooded.AffectsRoad() || TypeShelter.AffectsRoad() || TypeHelpNeeded.AffectsRoad() {
		t.Error("unexpected AffectsRoad classification")
	}
	if !TypeAidPoint.IsFacility() || TypeFlooded.IsFacility() {
		t.Error("unexpected IsFacility classification")
	}
	for ty := range typeRules {
		r := ty.DuplicateRadiusMeters()
		if r < 50 || r > 150 {
			t.Errorf("%s duplicate radius %v outside 50-150m", ty, r)
		}
	}
	// Legacy categories stay readable (list/nearby/duplicate filters accept
	// them) but new reports can't use them.
	for _, ty := range []Type{TypeVehicleStalled, TypeHelpNeeded, TypeOther} {
		if !ty.Valid() || ty.Creatable() {
			t.Errorf("%s should be valid for reads but not creatable", ty)
		}
	}
	for _, ty := range creatableTypes {
		if !Type(ty).Valid() || !Type(ty).Creatable() {
			t.Errorf("%s should be creatable", ty)
		}
	}
	if len(creatableTypes) != len(typeRules)-3 {
		t.Errorf("creatableTypes has %d entries, want every non-legacy category", len(creatableTypes))
	}
	if MaxDuplicateRadiusMeters() != 150 {
		t.Errorf("max duplicate radius = %v, want 150", MaxDuplicateRadiusMeters())
	}
	if !SeverityHigh.IsSevere() || !SeverityCritical.IsSevere() || SeverityModerate.IsSevere() {
		t.Error("unexpected IsSevere classification")
	}
}

func TestConfirmationInputValidate(t *testing.T) {
	t.Run("valid confirmation passes", func(t *testing.T) {
		in := NewConfirmationInput{DeviceID: "12345678-aaaa-bbbb-cccc-dddddddddddd", Status: StatusStillActive}
		if err := in.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("invalid status rejected", func(t *testing.T) {
		in := NewConfirmationInput{DeviceID: "12345678-aaaa-bbbb-cccc-dddddddddddd", Status: "bogus"}
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for invalid status")
		}
	})

	t.Run("too-short device id rejected", func(t *testing.T) {
		in := NewConfirmationInput{DeviceID: "short", Status: StatusCleared}
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for short device id")
		}
	})
}
