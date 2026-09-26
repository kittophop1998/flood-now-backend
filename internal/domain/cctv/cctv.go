// Package cctv holds official road cameras (currently only the Department
// of Highways' Highway Traffic system) as FloodNow shows them: an optional
// map layer that lets people look at a road for themselves. A camera says
// nothing about flooding on its own — FloodNow never infers a road
// condition from it — and it's always attributed to its provider.
package cctv

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	ProviderDOH = "DOH"
	SourceName  = "กรมทางหลวง Highway Traffic"
	// SourceURL is the public Highway Traffic map, where each camera's
	// pictures are shown. It has no per-camera address: a camera is found
	// there by its station code.
	SourceURL = "https://highwaytraffic.go.th/DOHWeb/Home.aspx"
)

// Mode is how a camera's pictures can be seen.
type Mode string

const (
	// ModeExternalLink: only on the provider's own page (ExternalURL).
	ModeExternalLink Mode = "external_link"
	// ModeSnapshot / ModeLiveStream: a still image / stream FloodNow may show
	// itself. Only set when the provider offers one for that use.
	ModeSnapshot   Mode = "snapshot"
	ModeLiveStream Mode = "live_stream"
)

// Status is whether the camera is known to be working.
type Status string

const (
	StatusOnline  Status = "online"
	StatusOffline Status = "offline"
	// StatusUnknown: the provider doesn't say, and FloodNow doesn't check.
	StatusUnknown Status = "unknown"
)

// Camera is one normalized camera. Optional text is "" when the provider
// didn't give it; nothing is invented.
type Camera struct {
	// ID is FloodNow's id for the camera, stable across catalog refreshes.
	ID         string
	ExternalID string
	// Name is the provider's label for the camera (DOH: the station code).
	Name           string
	Latitude       float64
	Longitude      float64
	Provider       string
	HighwayNumber  string
	ControlSection string
	KMMarker       string
	Mode           Mode
	ExternalURL    string
	Status         Status
}

var ErrInvalidCamera = errors.New("invalid camera")

// NewDOHCamera validates a Highway Traffic station and normalizes it.
func NewDOHCamera(siteID, code string, lat, lng float64, highway, section, km string) (Camera, error) {
	siteID, code = strings.TrimSpace(siteID), strings.TrimSpace(code)
	if siteID == "" || code == "" || !validPoint(lat, lng) {
		return Camera{}, ErrInvalidCamera
	}
	return Camera{
		ID:             "doh-" + siteID,
		ExternalID:     siteID,
		Name:           code,
		Latitude:       lat,
		Longitude:      lng,
		Provider:       ProviderDOH,
		HighwayNumber:  strings.TrimSpace(highway),
		ControlSection: strings.TrimSpace(section),
		KMMarker:       strings.TrimSpace(km),
		Mode:           ModeExternalLink,
		ExternalURL:    SourceURL,
		Status:         StatusUnknown,
	}, nil
}

// validPoint rejects out-of-range, non-finite and 0,0 ("no location")
// coordinates.
func validPoint(lat, lng float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lng) || math.IsInf(lat, 0) || math.IsInf(lng, 0) {
		return false
	}
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return false
	}
	return lat != 0 || lng != 0
}

// Catalog is every camera the provider listed at one fetch.
type Catalog struct {
	Cameras   []Camera
	FetchedAt time.Time
}

// Bounds is a lng/lat box.
type Bounds struct {
	MinLng, MinLat, MaxLng, MaxLat float64
}

func (b Bounds) Contains(lat, lng float64) bool {
	return lat >= b.MinLat && lat <= b.MaxLat && lng >= b.MinLng && lng <= b.MaxLng
}

// Within returns up to limit cameras inside view (all when view is nil);
// more is true when the limit cut the result short.
func (c *Catalog) Within(view *Bounds, limit int) (out []Camera, more bool) {
	out = []Camera{}
	for _, cam := range c.Cameras {
		if view != nil && !view.Contains(cam.Latitude, cam.Longitude) {
			continue
		}
		if len(out) >= limit {
			return out, true
		}
		out = append(out, cam)
	}
	return out, false
}

// Find returns the camera with FloodNow id id.
func (c *Catalog) Find(id string) (Camera, bool) {
	for _, cam := range c.Cameras {
		if cam.ID == id {
			return cam, true
		}
	}
	return Camera{}, false
}

// Near is a camera with its distance from a point.
type Near struct {
	Camera
	DistanceM float64
}

// Nearest returns up to limit cameras within radiusM meters of the point,
// closest first.
func (c *Catalog) Nearest(lat, lng, radiusM float64, limit int) []Near {
	out := []Near{}
	for _, cam := range c.Cameras {
		if d := DistanceM(lat, lng, cam.Latitude, cam.Longitude); d <= radiusM {
			out = append(out, Near{Camera: cam, DistanceM: d})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DistanceM < out[j].DistanceM })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// DistanceM is the great-circle (haversine) distance in meters.
func DistanceM(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6_371_000
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLng := rad(lat2-lat1), rad(lng2-lng1)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}
