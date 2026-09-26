// Package gistda implements ports.FloodAreaProvider against GISTDA's API
// gateway (GISTDA_BASE_URL, officially
// https://api-gateway.gistda.or.th/api/2.0/resources). It reads the flood
// Feature API, GET {base}/features/flood/{1day|3days|7days|30days}, which
// returns a GeoJSON FeatureCollection authenticated with the "API-Key"
// header. The key is sent only to the configured base URL and never appears
// in logs or errors.
package gistda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"floodnow-api/internal/domain/apperr"
	domain "floodnow-api/internal/domain/officialflood"
	"floodnow-api/internal/ports"
)

const (
	pageSize     = 1000
	maxPages     = 50       // hard cap: 50k features per period
	maxPageBytes = 64 << 20 // per response body
	retryDelay   = 2 * time.Second
	userAgent    = "FloodNow/1.0 (community flood map)"
)

var upstreamPeriod = map[domain.Period]string{
	domain.Period1D:  "1day",
	domain.Period3D:  "3days",
	domain.Period7D:  "7days",
	domain.Period30D: "30days",
}

type Client struct {
	baseURL string
	apiKey  string
	client  *http.Client
	clock   ports.Clock
}

// New returns a client; timeout bounds each upstream HTTP request.
func New(baseURL, apiKey string, timeout time.Duration, clock ports.Clock) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: timeout,
			// Never forward the key to another host.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && req.URL.Host != via[0].URL.Host {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		clock: clock,
	}
}

type featureCollection struct {
	Type          string    `json:"type"`
	Features      []feature `json:"features"`
	NumberMatched *int      `json:"numberMatched"`
}

type feature struct {
	ID       json.RawMessage `json:"id"`
	Geometry *struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
	Properties map[string]any `json:"properties"`
}

// errPermanent marks failures a retry can't fix (bad key, bad request);
// errBadRequest is the 400 case of it.
var (
	errPermanent  = errors.New("permanent upstream failure")
	errBadRequest = fmt.Errorf("upstream status 400: %w", errPermanent)
)

func (c *Client) FloodAreas(ctx context.Context, period domain.Period) (*domain.Snapshot, error) {
	up, ok := upstreamPeriod[period]
	if !ok {
		return nil, apperr.Validation("period is invalid", map[string]string{"period": "must be one of 1d, 3d, 7d, 30d"})
	}
	path := "/features/flood/" + up
	start := time.Now()

	var areas []domain.Area
	rejected, received := 0, 0
	var keys map[string]bool
	paged := true
	for page := 0; ; page++ {
		if page >= maxPages {
			log.Printf("gistda: provider=GISTDA period=%s stopped after %d pages (%d features); result truncated", period, maxPages, received)
			break
		}
		body, err := c.fetchPage(ctx, path, page*pageSize, paged)
		if err != nil && page == 0 && paged && errors.Is(err, errBadRequest) {
			// A gateway that rejects limit/offset: ask once for the whole
			// collection instead.
			log.Printf("gistda: provider=GISTDA period=%s paging rejected (400); retrying without limit/offset", period)
			paged = false
			body, err = c.fetchPage(ctx, path, 0, false)
		}
		if err != nil {
			log.Printf("gistda: provider=GISTDA period=%s failed after %dms: %v", period, time.Since(start).Milliseconds(), err)
			return nil, apperr.Unavailable("official flood data is unavailable right now")
		}
		if body.Type != "FeatureCollection" {
			log.Printf("gistda: provider=GISTDA period=%s invalid response: type=%q", period, body.Type)
			return nil, apperr.Unavailable("official flood data returned an unexpected response")
		}
		received += len(body.Features)
		for _, f := range body.Features {
			if keys == nil && f.Properties != nil {
				keys = map[string]bool{}
				for k := range f.Properties {
					keys[k] = true
				}
			}
			a, err := normalize(f)
			if err != nil {
				rejected++
				continue
			}
			areas = append(areas, a)
		}
		// Page until the provider says we have everything (numberMatched) or
		// a short page shows there's nothing more. A provider ignoring
		// limit/offset returns everything in one page and stops here too.
		if !paged {
			break
		}
		if body.NumberMatched != nil {
			if received >= *body.NumberMatched || len(body.Features) == 0 {
				break
			}
		} else if len(body.Features) < pageSize {
			break
		}
	}
	if received > 0 && len(areas) == 0 {
		log.Printf("gistda: provider=GISTDA period=%s invalid response: all %d features rejected", period, received)
		return nil, apperr.Unavailable("official flood data returned an unexpected response")
	}
	log.Printf("gistda: provider=GISTDA period=%s latency=%dms features=%d normalized=%d rejected=%d property_keys=%v",
		period, time.Since(start).Milliseconds(), received, len(areas), rejected, sortedKeys(keys))
	return domain.NewSnapshot(period, areas, c.clock.Now()), nil
}

// fetchPage does one GET with at most one retry for transient failures
// (network error, 429, 5xx). Auth or request errors are not retried.
func (c *Client) fetchPage(ctx context.Context, path string, offset int, paged bool) (*featureCollection, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		body, err := c.get(ctx, path, offset, paged)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if errors.Is(err, errPermanent) || ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) get(ctx context.Context, path string, offset int, paged bool) (*featureCollection, error) {
	u := c.baseURL + path
	if paged {
		u += "?" + url.Values{"limit": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(offset)}}.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", errPermanent)
	}
	req.Header.Set("API-Key", c.apiKey)
	req.Header.Set("Accept", "application/geo+json, application/json")
	req.Header.Set("User-Agent", userAgent)

	t := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		// Only the kind of failure: *url.Error would repeat the URL, and
		// net errors never carry headers, but keep the log minimal anyway.
		kind := "network error"
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			kind = "timeout"
		}
		log.Printf("gistda: GET %s offset=%d %s after %dms", path, offset, kind, time.Since(t).Milliseconds())
		return nil, errors.New(kind)
	}
	defer resp.Body.Close()
	log.Printf("gistda: GET %s offset=%d status=%d latency=%dms", path, offset, resp.StatusCode, time.Since(t).Milliseconds())

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	case resp.StatusCode == http.StatusBadRequest:
		return nil, errBadRequest
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusProxyAuthRequired:
		return nil, fmt.Errorf("upstream rejected the API key (status %d; check GISTDA_API_KEY): %w", resp.StatusCode, errPermanent)
	default:
		return nil, fmt.Errorf("upstream status %d: %w", resp.StatusCode, errPermanent)
	}

	var body featureCollection
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, maxPageBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("undecodable response body: %w", errPermanent)
	}
	return &body, nil
}

// normalize keeps only what FloodNow shows: the (multi)polygon, the
// provider's feature id, and a date the provider attached — nothing else of
// the provider's schema reaches clients.
func normalize(f feature) (domain.Area, error) {
	if f.Geometry == nil {
		return domain.Area{}, domain.ErrInvalidGeometry
	}
	var polys []domain.Polygon
	switch f.Geometry.Type {
	case "Polygon":
		var rings [][][]float64
		if err := json.Unmarshal(f.Geometry.Coordinates, &rings); err != nil {
			return domain.Area{}, domain.ErrInvalidGeometry
		}
		p, err := toPolygon(rings)
		if err != nil {
			return domain.Area{}, err
		}
		polys = []domain.Polygon{p}
	case "MultiPolygon":
		var multi [][][][]float64
		if err := json.Unmarshal(f.Geometry.Coordinates, &multi); err != nil {
			return domain.Area{}, domain.ErrInvalidGeometry
		}
		for _, rings := range multi {
			p, err := toPolygon(rings)
			if err != nil {
				return domain.Area{}, err
			}
			polys = append(polys, p)
		}
	default:
		return domain.Area{}, domain.ErrInvalidGeometry
	}
	return domain.NewArea(featureID(f.ID), polys, observedAt(f.Properties))
}

func toPolygon(rings [][][]float64) (domain.Polygon, error) {
	if len(rings) == 0 {
		return nil, domain.ErrInvalidGeometry
	}
	out := make(domain.Polygon, len(rings))
	for i, ring := range rings {
		r := make(domain.Ring, len(ring))
		for j, pos := range ring {
			if len(pos) < 2 {
				return nil, domain.ErrInvalidGeometry
			}
			r[j] = domain.Point{pos[0], pos[1]}
		}
		out[i] = r
	}
	return out, nil
}

func featureID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

var dateLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

// observedAt returns the latest date among the feature's properties whose
// name contains "date" (the flood-extent acquisition date); nil when there
// is none. Values without a zone are read as Thai time (UTC+7).
func observedAt(props map[string]any) *time.Time {
	bkk := time.FixedZone("ICT", 7*3600)
	var best *time.Time
	for k, v := range props {
		s, ok := v.(string)
		if !ok || !strings.Contains(strings.ToLower(k), "date") {
			continue
		}
		for _, layout := range dateLayouts {
			t, err := time.ParseInLocation(layout, strings.TrimSpace(s), bkk)
			if err == nil {
				t = t.UTC()
				if best == nil || t.After(*best) {
					best = &t
				}
				break
			}
		}
	}
	return best
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
