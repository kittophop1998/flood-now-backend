// Package routing implements ports.RouteProvider against the OSRM HTTP API
// (the public routing.openstreetmap.de instances by default; point
// ROUTER_URL / ROUTER_FOOT_URL at a self-hosted OSRM for production).
// FloodNow never computes routes itself — it only scores what OSRM returns.
package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/route"
)

const (
	cacheTTL     = 2 * time.Minute // road geometry doesn't change; keep repeat taps cheap
	cacheMaxSize = 500
)

type OSRM struct {
	drivingURL string // base URL serving /route/v1/driving
	footURL    string // base URL serving /route/v1/foot
	userAgent  string
	client     *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	routes  []route.Candidate
	expires time.Time
}

func NewOSRM(drivingURL, footURL, userAgent string) *OSRM {
	return &OSRM{
		drivingURL: drivingURL,
		footURL:    footURL,
		userAgent:  userAgent,
		client:     &http.Client{Timeout: 8 * time.Second},
		cache:      map[string]cacheEntry{},
	}
}

type osrmResponse struct {
	Code   string `json:"code"`
	Routes []struct {
		Distance float64 `json:"distance"`
		Duration float64 `json:"duration"`
		Geometry struct {
			Coordinates [][2]float64 `json:"coordinates"` // [lng, lat]
		} `json:"geometry"`
	} `json:"routes"`
}

func coord(p route.Point) string {
	return strconv.FormatFloat(p.Longitude, 'f', 6, 64) + "," + strconv.FormatFloat(p.Latitude, 'f', 6, 64)
}

func (o *OSRM) Routes(ctx context.Context, origin, destination route.Point, profile route.Profile) ([]route.Candidate, error) {
	base := o.drivingURL
	if profile == route.ProfileFoot {
		base = o.footURL
	}
	u := fmt.Sprintf("%s/route/v1/%s/%s;%s?alternatives=true&overview=full&geometries=geojson&steps=false",
		base, profile, coord(origin), coord(destination))

	o.mu.Lock()
	if e, ok := o.cache[u]; ok && time.Now().Before(e.expires) {
		o.mu.Unlock()
		return e.routes, nil
	}
	o.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build routing request: %w", err)
	}
	req.Header.Set("User-Agent", o.userAgent)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, apperr.Unavailable("route service is unavailable")
	}
	defer resp.Body.Close()

	var body osrmResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, apperr.Unavailable("route service returned an unexpected response")
	}
	// OSRM answers 400 with code NoRoute/NoSegment when the points can't be
	// connected; that's "no route", not an outage.
	switch body.Code {
	case "Ok":
	case "NoRoute", "NoSegment":
		return []route.Candidate{}, nil
	default:
		return nil, apperr.Unavailable("route service is unavailable")
	}

	out := make([]route.Candidate, 0, len(body.Routes))
	for _, r := range body.Routes {
		c := route.Candidate{DistanceM: r.Distance, DurationS: r.Duration, Path: make([]route.Point, len(r.Geometry.Coordinates))}
		for i, ll := range r.Geometry.Coordinates {
			c.Path[i] = route.Point{Latitude: ll[1], Longitude: ll[0]}
		}
		out = append(out, c)
	}

	o.mu.Lock()
	if len(o.cache) >= cacheMaxSize {
		o.cache = map[string]cacheEntry{}
	}
	o.cache[u] = cacheEntry{routes: out, expires: time.Now().Add(cacheTTL)}
	o.mu.Unlock()
	return out, nil
}
