// Package dohtraffic implements ports.CCTVProvider against the Department
// of Highways' public Highway Traffic map (DOH_CCTV_BASE_URL, officially
// https://highwaytraffic.go.th). DOH publishes no camera API; its public
// map page, GET {base}/DOHWeb/Home.aspx, carries every station camera
// inline — a map pin per station (site id, code, lat/lng) and a station
// table (highway number, control section, km marker). One anonymous GET of
// that page per cache TTL is the whole integration: no login, no cookies,
// and none of the page's internal AJAX methods or video streams are used.
package dohtraffic

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"floodnow-api/internal/domain/apperr"
	domain "floodnow-api/internal/domain/cctv"
	"floodnow-api/internal/ports"
)

const (
	pagePath     = "/DOHWeb/Home.aspx"
	maxPageBytes = 8 << 20
	retryDelay   = 2 * time.Second
	userAgent    = "FloodNow/1.0 (community flood map)"
)

type Client struct {
	baseURL string
	client  *http.Client
	clock   ports.Clock
}

// New returns a client; timeout bounds each upstream HTTP request.
func New(baseURL string, timeout time.Duration, clock ports.Clock) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
		clock:   clock,
	}
}

var errPermanent = errors.New("permanent upstream failure")

func (c *Client) Cameras(ctx context.Context) (*domain.Catalog, error) {
	start := time.Now()
	page, err := c.fetch(ctx)
	if err != nil {
		log.Printf("dohtraffic: provider=DOH failed after %dms: %v", time.Since(start).Milliseconds(), err)
		return nil, apperr.Unavailable("highway camera data is unavailable right now")
	}
	cams, pins, rejected := parse(page)
	if len(cams) == 0 {
		log.Printf("dohtraffic: provider=DOH invalid response: pins=%d rejected=%d", pins, rejected)
		return nil, apperr.Unavailable("highway camera data returned an unexpected response")
	}
	log.Printf("dohtraffic: provider=DOH latency=%dms pins=%d cameras=%d rejected=%d",
		time.Since(start).Milliseconds(), pins, len(cams), rejected)
	return &domain.Catalog{Cameras: cams, FetchedAt: c.clock.Now()}, nil
}

// fetch does one GET with at most one retry for transient failures
// (network error, 429, 5xx).
func (c *Client) fetch(ctx context.Context) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		body, err := c.get(ctx)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if errors.Is(err, errPermanent) || ctx.Err() != nil {
			break
		}
	}
	return "", lastErr
}

func (c *Client) get(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+pagePath, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", errPermanent)
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", userAgent)

	t := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		kind := "network error"
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			kind = "timeout"
		}
		log.Printf("dohtraffic: GET %s %s after %dms", pagePath, kind, time.Since(t).Milliseconds())
		return "", errors.New(kind)
	}
	defer resp.Body.Close()
	log.Printf("dohtraffic: GET %s status=%d latency=%dms", pagePath, resp.StatusCode, time.Since(t).Milliseconds())

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return "", fmt.Errorf("upstream status %d", resp.StatusCode)
	default:
		return "", fmt.Errorf("upstream status %d: %w", resp.StatusCode, errPermanent)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return "", errors.New("read error")
	}
	if len(b) > maxPageBytes {
		return "", fmt.Errorf("response larger than %d bytes: %w", maxPageBytes, errPermanent)
	}
	return string(b), nil
}

var (
	// CreateCustomDiv(13.8496, 99.9771, 'Tmp-080', '<div id="pin3846" onclick="MoveLocation(3846);" …
	pinRe = regexp.MustCompile(`CreateCustomDiv\(\s*(-?[\d.]+)\s*,\s*(-?[\d.]+)\s*,\s*'([^']*)'\s*,\s*'[^']*?MoveLocation\((\d+)\)`)
	// <a href='javascript:MoveLocation2(3846)'>Tmp-080</a></td><td>81</td><td>0100</td><td><span …>48+300</span>
	rowRe = regexp.MustCompile(`(?s)MoveLocation2\((\d+)\)'>[^<]*</a>\s*</td>\s*<td>([^<]*)</td>\s*<td>([^<]*)</td>\s*<td>\s*(?:<span[^>]*>)?([^<]*)`)
)

type station struct{ highway, section, km string }

// parse extracts the cameras from the page. Stations missing from the table
// are kept without road details; pins with an unusable id or position are
// dropped (counted in rejected). Duplicate site ids keep the first pin.
func parse(page string) (cams []domain.Camera, pins, rejected int) {
	details := map[string]station{}
	for _, m := range rowRe.FindAllStringSubmatch(page, -1) {
		details[m[1]] = station{clean(m[2]), clean(m[3]), clean(m[4])}
	}
	seen := map[string]bool{}
	for _, m := range pinRe.FindAllStringSubmatch(page, -1) {
		pins++
		lat, errLat := strconv.ParseFloat(m[1], 64)
		lng, errLng := strconv.ParseFloat(m[2], 64)
		siteID := m[4]
		if errLat != nil || errLng != nil || seen[siteID] {
			rejected++
			continue
		}
		d := details[siteID]
		cam, err := domain.NewDOHCamera(siteID, clean(m[3]), lat, lng, d.highway, d.section, d.km)
		if err != nil {
			rejected++
			continue
		}
		seen[siteID] = true
		cams = append(cams, cam)
	}
	return cams, pins, rejected
}

// clean unescapes HTML text and drops the page's placeholders for "none".
func clean(s string) string {
	s = strings.TrimSpace(html.UnescapeString(s))
	switch strings.ToUpper(s) {
	case "-", "N/A", "&NBSP;", " ":
		return ""
	}
	return s
}
