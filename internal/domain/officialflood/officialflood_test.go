package officialflood

import (
	"math"
	"testing"
)

func circle(lng, lat, r float64, n int) Ring {
	ring := make(Ring, 0, n+1)
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		ring = append(ring, Point{lng + r*math.Cos(a), lat + r*math.Sin(a)})
	}
	return append(ring, ring[0])
}

func TestNewAreaValidates(t *testing.T) {
	if _, err := NewArea("", nil, nil); err == nil {
		t.Error("empty geometry accepted")
	}
	if _, err := NewArea("", []Polygon{{{{100, 13}, {100, 14}, {100, 13}}}}, nil); err == nil {
		t.Error("3-position ring accepted")
	}
	if _, err := NewArea("", []Polygon{{{{100, 95}, {101, 13}, {101, 14}, {100, 95}}}}, nil); err == nil {
		t.Error("out-of-range latitude accepted")
	}
	a, err := NewArea("x", []Polygon{{{{100, 13}, {101, 13}, {101, 14}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Polygons[0][0]; len(r) != 4 || r[0] != r[3] {
		t.Errorf("open ring not closed: %v", r)
	}
	if a.Bounds != (Bounds{100, 13, 101, 14}) {
		t.Errorf("bounds = %+v", a.Bounds)
	}
}

func TestPeriods(t *testing.T) {
	for _, p := range Periods {
		if !p.Valid() {
			t.Errorf("%s invalid", p)
		}
	}
	if Period("1day").Valid() || DefaultPeriod != Period1D {
		t.Error("period set wrong")
	}
}

func TestClipFiltersSimplifiesAndLimits(t *testing.T) {
	big, _ := NewArea("", []Polygon{{circle(100.5, 13.75, 0.2, 400)}}, nil)
	far, _ := NewArea("", []Polygon{{circle(104, 17, 0.2, 50)}}, nil)
	tiny, _ := NewArea("", []Polygon{{circle(100.3, 13.6, 0.00001, 20)}}, nil)
	areas := []Area{big, far, tiny}

	view := Bounds{100, 13, 101, 14.5}
	out, more := Clip(areas, view, ToleranceFor(view), ClipLimits{MaxAreas: 10, MaxVertices: 10000})
	if more || len(out) != 1 || out[0].Ref != 0 {
		t.Fatalf("out=%d more=%v", len(out), more)
	}
	simplified := out[0].Polygons[0][0]
	if len(simplified) >= 400 || len(simplified) < 8 {
		t.Errorf("ring has %d points after simplification", len(simplified))
	}
	if simplified[0] != simplified[len(simplified)-1] {
		t.Error("simplified ring not closed")
	}
	if len(areas[0].Polygons[0][0]) != 401 {
		t.Error("clip must not modify the cached areas")
	}

	// Zoomed in, more of the detail is kept.
	near := Bounds{100.68, 13.7, 100.72, 13.73}
	out, _ = Clip(areas, near, ToleranceFor(near), ClipLimits{MaxAreas: 10, MaxVertices: 10000})
	if len(out) != 1 || len(out[0].Polygons[0][0]) <= 2*len(simplified) {
		t.Errorf("zoomed-in ring kept %d points vs %d zoomed out", len(out[0].Polygons[0][0]), len(simplified))
	}

	// Limits truncate and say so.
	all := Bounds{99, 12, 105, 18}
	out, more = Clip(areas, all, 1e-5, ClipLimits{MaxAreas: 1, MaxVertices: 10000})
	if !more || len(out) != 1 {
		t.Errorf("area limit: out=%d more=%v", len(out), more)
	}
	out, more = Clip(areas, all, 1e-5, ClipLimits{MaxAreas: 10, MaxVertices: 100})
	if !more || len(out) != 0 {
		t.Errorf("vertex limit: out=%d more=%v", len(out), more)
	}
}
