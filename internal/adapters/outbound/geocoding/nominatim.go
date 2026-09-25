// Package geocoding implements ports.Geocoder against a Nominatim-compatible
// API (public OSM Nominatim by default; point GEOCODER_URL at a self-hosted
// or commercial instance for production traffic).
package geocoding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/place"
	"floodnow-api/internal/ports"
)

const (
	cacheTTL     = 24 * time.Hour
	cacheMaxSize = 2000
	// The public Nominatim usage policy allows at most 1 request/second
	// per application; requests are serialized to honor that.
	minInterval = time.Second
)

type Nominatim struct {
	baseURL   string
	userAgent string
	client    *http.Client

	mu       sync.Mutex // guards cache and lastCall
	cache    map[string]cacheEntry
	lastCall time.Time
	throttle sync.Mutex // serializes upstream calls
}

type cacheEntry struct {
	body    []byte
	expires time.Time
}

func NewNominatim(baseURL, userAgent string) *Nominatim {
	return &Nominatim{
		baseURL:   baseURL,
		userAgent: userAgent,
		client:    &http.Client{Timeout: 6 * time.Second},
		cache:     map[string]cacheEntry{},
	}
}

type nominatimResult struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
}

func (r nominatimResult) toPlace() (place.Place, bool) {
	lat, err1 := strconv.ParseFloat(r.Lat, 64)
	lng, err2 := strconv.ParseFloat(r.Lon, 64)
	if err1 != nil || err2 != nil {
		return place.Place{}, false
	}
	name := r.Name
	if name == "" {
		name = r.DisplayName
	}
	return place.Place{Name: name, DisplayName: r.DisplayName, Latitude: lat, Longitude: lng}, true
}

func (n *Nominatim) Search(ctx context.Context, query, lang string, near *ports.BBox, limit int) ([]place.Place, error) {
	params := url.Values{
		"q":               {query},
		"format":          {"jsonv2"},
		"limit":           {strconv.Itoa(limit)},
		"accept-language": {lang},
	}
	if near != nil {
		// Prefer (not restrict to) results in the visible area.
		params.Set("viewbox", fmt.Sprintf("%f,%f,%f,%f", near.MinLng, near.MaxLat, near.MaxLng, near.MinLat))
	}

	var results []nominatimResult
	if err := n.get(ctx, "/search", params, &results); err != nil {
		return nil, err
	}
	places := make([]place.Place, 0, len(results))
	for _, r := range results {
		if p, ok := r.toPlace(); ok {
			places = append(places, p)
		}
	}
	return places, nil
}

func (n *Nominatim) Reverse(ctx context.Context, lat, lng float64, lang string) (*place.Place, error) {
	// Round to ~11 m so nearby lookups share a cache entry.
	params := url.Values{
		"lat":             {strconv.FormatFloat(lat, 'f', 4, 64)},
		"lon":             {strconv.FormatFloat(lng, 'f', 4, 64)},
		"format":          {"jsonv2"},
		"zoom":            {"17"},
		"accept-language": {lang},
	}
	var result struct {
		nominatimResult
		Error string `json:"error"`
	}
	if err := n.get(ctx, "/reverse", params, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, nil
	}
	p, ok := result.toPlace()
	if !ok {
		return nil, nil
	}
	return &p, nil
}

// get fetches path?params into out, served from cache when possible.
func (n *Nominatim) get(ctx context.Context, path string, params url.Values, out any) error {
	u := n.baseURL + path + "?" + params.Encode()

	n.mu.Lock()
	if e, ok := n.cache[u]; ok && time.Now().Before(e.expires) {
		n.mu.Unlock()
		return json.Unmarshal(e.body, out)
	}
	n.mu.Unlock()

	n.throttle.Lock()
	n.mu.Lock()
	wait := minInterval - time.Since(n.lastCall)
	n.mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			n.throttle.Unlock()
			return apperr.Unavailable("location search timed out")
		}
	}
	n.mu.Lock()
	n.lastCall = time.Now()
	n.mu.Unlock()
	body, err := n.fetch(ctx, u)
	n.throttle.Unlock()
	if err != nil {
		return err
	}

	if err := json.Unmarshal(body, out); err != nil {
		return apperr.Unavailable("location service returned an unexpected response")
	}

	n.mu.Lock()
	if len(n.cache) >= cacheMaxSize {
		n.cache = map[string]cacheEntry{} // crude but bounded; entries are cheap to refetch
	}
	n.cache[u] = cacheEntry{body: body, expires: time.Now().Add(cacheTTL)}
	n.mu.Unlock()
	return nil
}

func (n *Nominatim) fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build geocoder request: %w", err)
	}
	req.Header.Set("User-Agent", n.userAgent)
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, apperr.Unavailable("location service is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apperr.Unavailable("location service is unavailable")
	}
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&raw); err != nil {
		return nil, apperr.Unavailable("location service returned an unexpected response")
	}
	return raw, nil
}
