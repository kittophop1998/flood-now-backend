// Package officialflood holds official flood-area data (GISTDA satellite
// flood extents) as FloodNow shows it: an optional map layer, kept apart
// from community reports. An area only says "satellite data showed water
// here during the period" — never a road depth, a closure or whether a
// vehicle can pass; those remain community reports.
package officialflood

import (
	"errors"
	"math"
	"time"
)

const (
	SourceName = "GISTDA"
	// SourceURL is GISTDA's public disaster platform, linked as the source.
	SourceURL = "https://disaster.gistda.or.th"
)

// Period is the rolling window the flood extent covers.
type Period string

const (
	Period1D  Period = "1d"
	Period3D  Period = "3d"
	Period7D  Period = "7d"
	Period30D Period = "30d"
)

// DefaultPeriod is the latest day.
const DefaultPeriod = Period1D

var Periods = []Period{Period1D, Period3D, Period7D, Period30D}

func (p Period) Valid() bool {
	switch p {
	case Period1D, Period3D, Period7D, Period30D:
		return true
	}
	return false
}

// Point is [lng, lat] (GeoJSON order).
type Point = [2]float64

// Ring is a closed linear ring; Polygon is an outer ring followed by holes.
type Ring = []Point
type Polygon = []Ring

// Bounds is a lng/lat box.
type Bounds struct {
	MinLng, MinLat, MaxLng, MaxLat float64
}

func (b Bounds) Intersects(o Bounds) bool {
	return b.MinLng <= o.MaxLng && o.MinLng <= b.MaxLng && b.MinLat <= o.MaxLat && o.MinLat <= b.MaxLat
}

func (b Bounds) extend(o Bounds) Bounds {
	return Bounds{math.Min(b.MinLng, o.MinLng), math.Min(b.MinLat, o.MinLat), math.Max(b.MaxLng, o.MaxLng), math.Max(b.MaxLat, o.MaxLat)}
}

// Area is one flood-extent feature (a multipolygon).
type Area struct {
	// ID is the provider's feature id, empty when it sent none.
	ID       string
	Polygons []Polygon
	// ObservedAt is the date the provider attached to the feature, if any.
	ObservedAt *time.Time
	Bounds     Bounds
}

// Snapshot is one period's flood areas as fetched from the provider.
type Snapshot struct {
	Period Period
	Areas  []Area
	// ObservedAt is the latest ObservedAt of the areas (nil if none had one).
	ObservedAt *time.Time
	FetchedAt  time.Time
}

var ErrInvalidGeometry = errors.New("invalid flood-area geometry")

// NewArea validates polygons (lng/lat in range, finite, rings of at least 4
// positions) and computes the bounds. Rings are closed if the provider left
// them open.
func NewArea(id string, polygons []Polygon, observedAt *time.Time) (Area, error) {
	if len(polygons) == 0 {
		return Area{}, ErrInvalidGeometry
	}
	b := Bounds{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for pi, poly := range polygons {
		if len(poly) == 0 {
			return Area{}, ErrInvalidGeometry
		}
		for ri, ring := range poly {
			for _, p := range ring {
				if math.IsNaN(p[0]) || math.IsNaN(p[1]) || p[0] < -180 || p[0] > 180 || p[1] < -90 || p[1] > 90 {
					return Area{}, ErrInvalidGeometry
				}
				b = b.extend(Bounds{p[0], p[1], p[0], p[1]})
			}
			if len(ring) > 0 && ring[0] != ring[len(ring)-1] {
				ring = append(ring, ring[0])
				polygons[pi][ri] = ring
			}
			if len(ring) < 4 {
				return Area{}, ErrInvalidGeometry
			}
		}
	}
	return Area{ID: id, Polygons: polygons, ObservedAt: observedAt, Bounds: b}, nil
}

// NewSnapshot builds a snapshot, deriving its ObservedAt from the areas.
func NewSnapshot(period Period, areas []Area, fetchedAt time.Time) *Snapshot {
	s := &Snapshot{Period: period, Areas: areas, FetchedAt: fetchedAt}
	for _, a := range areas {
		if a.ObservedAt != nil && (s.ObservedAt == nil || a.ObservedAt.After(*s.ObservedAt)) {
			t := *a.ObservedAt
			s.ObservedAt = &t
		}
	}
	return s
}

// Extent is the bounds of all areas (ok false when there are none).
func (s *Snapshot) Extent() (Bounds, bool) {
	if len(s.Areas) == 0 {
		return Bounds{}, false
	}
	b := s.Areas[0].Bounds
	for _, a := range s.Areas[1:] {
		b = b.extend(a.Bounds)
	}
	return b, true
}

// ClipLimits bounds how much geometry one response carries.
type ClipLimits struct {
	MaxAreas    int
	MaxVertices int
}

// ClippedArea is an area prepared for a viewport; Ref is its index in the
// snapshot, stable for the snapshot's lifetime.
type ClippedArea struct {
	Area
	Ref int
}

// ToleranceFor is the simplification tolerance (degrees) for a view: about
// one pixel of a ~1000 px wide map, so shapes look the same but carry far
// fewer points when zoomed out.
func ToleranceFor(view Bounds) float64 {
	span := math.Max(view.MaxLng-view.MinLng, view.MaxLat-view.MinLat)
	return math.Max(span/1000, 1e-5)
}

// Clip returns the areas intersecting view, simplified to tolerance and
// rounded to 5 decimals (~1 m). Rings that collapse at this scale are
// dropped. truncated is true when limits cut the result short.
func Clip(areas []Area, view Bounds, tolerance float64, limits ClipLimits) (out []ClippedArea, truncated bool) {
	out = []ClippedArea{}
	vertices := 0
	for i, a := range areas {
		if !a.Bounds.Intersects(view) {
			continue
		}
		polys := make([]Polygon, 0, len(a.Polygons))
		n := 0
		for _, poly := range a.Polygons {
			outer := simplifyRing(poly[0], tolerance)
			if outer == nil {
				continue // smaller than a pixel here
			}
			p := Polygon{outer}
			for _, hole := range poly[1:] {
				if h := simplifyRing(hole, tolerance); h != nil {
					p = append(p, h)
				}
			}
			for _, r := range p {
				n += len(r)
			}
			polys = append(polys, p)
		}
		if len(polys) == 0 {
			continue
		}
		if len(out) >= limits.MaxAreas || vertices+n > limits.MaxVertices {
			return out, true
		}
		vertices += n
		a.Polygons = polys // a is a copy; the snapshot keeps full detail
		out = append(out, ClippedArea{Area: a, Ref: i})
	}
	return out, false
}

func round5(v float64) float64 { return math.Round(v*1e5) / 1e5 }

// simplifyRing applies Douglas–Peucker to a closed ring, returning nil if
// what's left isn't a ring any more.
func simplifyRing(ring Ring, tolerance float64) Ring {
	if len(ring) < 4 {
		return nil
	}
	keep := make([]bool, len(ring))
	keep[0], keep[len(ring)-1] = true, true
	// A closed ring's endpoints coincide; split at the point farthest from
	// the start so the recursion has a real baseline.
	far, farD := 0, -1.0
	for i := 1; i < len(ring)-1; i++ {
		if d := sqDist(ring[i], ring[0]); d > farD {
			far, farD = i, d
		}
	}
	keep[far] = true
	dp(ring, 0, far, tolerance*tolerance, keep)
	dp(ring, far, len(ring)-1, tolerance*tolerance, keep)

	out := make(Ring, 0, len(ring))
	for i, p := range ring {
		if !keep[i] {
			continue
		}
		p = Point{round5(p[0]), round5(p[1])}
		if len(out) > 0 && out[len(out)-1] == p {
			continue
		}
		out = append(out, p)
	}
	if len(out) < 4 {
		return nil
	}
	return out
}

// dp marks the points between first and last that must stay within
// sqTol of the simplified line. Iterative to avoid deep recursion on
// large rings.
func dp(ring Ring, first, last int, sqTol float64, keep []bool) {
	stack := [][2]int{{first, last}}
	for len(stack) > 0 {
		seg := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		a, b := seg[0], seg[1]
		idx, maxD := -1, sqTol
		for i := a + 1; i < b; i++ {
			if d := sqSegDist(ring[i], ring[a], ring[b]); d > maxD {
				idx, maxD = i, d
			}
		}
		if idx >= 0 {
			keep[idx] = true
			stack = append(stack, [2]int{a, idx}, [2]int{idx, b})
		}
	}
}

func sqDist(p, q Point) float64 {
	dx, dy := p[0]-q[0], p[1]-q[1]
	return dx*dx + dy*dy
}

func sqSegDist(p, a, b Point) float64 {
	x, y := a[0], a[1]
	dx, dy := b[0]-x, b[1]-y
	if dx != 0 || dy != 0 {
		t := ((p[0]-x)*dx + (p[1]-y)*dy) / (dx*dx + dy*dy)
		if t > 1 {
			x, y = b[0], b[1]
		} else if t > 0 {
			x += dx * t
			y += dy * t
		}
	}
	dx, dy = p[0]-x, p[1]-y
	return dx*dx + dy*dy
}
