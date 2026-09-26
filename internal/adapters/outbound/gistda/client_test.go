package gistda

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"floodnow-api/internal/domain/apperr"
	domain "floodnow-api/internal/domain/officialflood"
)

const testKey = "test-secret-gistda-key"

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var now = time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)

func newClient(url string) *Client {
	return New(url, testKey, 2*time.Second, fixedClock{now})
}

// captureLog collects log output for the test's duration.
func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

const oneFeature = `{"type":"FeatureCollection","features":[
 {"type":"Feature","id":"f1","geometry":{"type":"Polygon","coordinates":[[[100.5,13.7],[100.6,13.7],[100.6,13.8],[100.5,13.8],[100.5,13.7]]]},
  "properties":{"f_date":"2026-09-25","area_rai":12.5,"secret":"x"}},
 {"type":"Feature","geometry":{"type":"MultiPolygon","coordinates":[[[[101,14],[101.1,14],[101.1,14.1],[101,14]]]]},"properties":{}}
]}`

func TestFloodAreasSendsKeyAndNormalizes(t *testing.T) {
	var gotPath, gotKey, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotQuery = r.URL.Path, r.Header.Get("API-Key"), r.URL.RawQuery
		w.Write([]byte(oneFeature))
	}))
	defer srv.Close()

	snap, err := newClient(srv.URL+"/api/2.0/resources/").FloodAreas(context.Background(), domain.Period3D)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/2.0/resources/features/flood/3days" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != testKey {
		t.Errorf("API-Key header = %q", gotKey)
	}
	if !strings.Contains(gotQuery, "limit=1000") || !strings.Contains(gotQuery, "offset=0") {
		t.Errorf("query = %q", gotQuery)
	}
	if len(snap.Areas) != 2 || snap.Period != domain.Period3D || !snap.FetchedAt.Equal(now) {
		t.Fatalf("snapshot = %+v", snap)
	}
	a := snap.Areas[0]
	if a.ID != "f1" || len(a.Polygons) != 1 || a.Bounds.MinLng != 100.5 || a.Bounds.MaxLat != 13.8 {
		t.Errorf("area = %+v", a)
	}
	// Date-only values are Thai dates (UTC+7).
	want := time.Date(2026, 9, 24, 17, 0, 0, 0, time.UTC)
	if a.ObservedAt == nil || !a.ObservedAt.Equal(want) || snap.ObservedAt == nil || !snap.ObservedAt.Equal(want) {
		t.Errorf("observed = %v / %v", a.ObservedAt, snap.ObservedAt)
	}
	// The open ring of the second feature was closed; no id, no date.
	b := snap.Areas[1]
	if b.ID != "" || b.ObservedAt != nil || len(b.Polygons[0][0]) != 4 {
		t.Errorf("second area = %+v", b)
	}
}

func TestFloodAreasPagesUsingNumberMatched(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			if r.URL.Query().Get("offset") != "0" {
				t.Errorf("first offset = %s", r.URL.Query().Get("offset"))
			}
			// The provider capped the page below our limit; numberMatched
			// says there's more.
			w.Write([]byte(strings.Replace(oneFeature, `"features"`, `"numberMatched":3,"features"`, 1)))
			return
		}
		if r.URL.Query().Get("offset") != "1000" {
			t.Errorf("second offset = %s", r.URL.Query().Get("offset"))
		}
		w.Write([]byte(`{"type":"FeatureCollection","numberMatched":3,"features":[{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[100,13],[100.1,13],[100.1,13.1],[100,13]]]}}]}`))
	}))
	defer srv.Close()

	snap, err := newClient(srv.URL).FloodAreas(context.Background(), domain.Period1D)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(snap.Areas) != 3 {
		t.Fatalf("calls=%d areas=%d", calls.Load(), len(snap.Areas))
	}
}

func TestFloodAreasFallsBackWhenPagingIsRejected(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Has("limit") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(oneFeature))
	}))
	defer srv.Close()
	snap, err := newClient(srv.URL).FloodAreas(context.Background(), domain.Period1D)
	if err != nil || len(snap.Areas) != 2 {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if len(queries) != 2 || queries[1] != "" {
		t.Errorf("queries = %q", queries)
	}
}

func TestFloodAreasRejectsBadFeaturesIndividually(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"FeatureCollection","features":[
		 {"type":"Feature","geometry":{"type":"Point","coordinates":[100,13]}},
		 {"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[200,13],[100.1,13],[100.1,13.1],[200,13]]]}},
		 {"type":"Feature","geometry":null},
		 {"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[100,13],[100.1,13],[100.1,13.1],[100,13]]]}}]}`))
	}))
	defer srv.Close()
	snap, err := newClient(srv.URL).FloodAreas(context.Background(), domain.Period1D)
	if err != nil || len(snap.Areas) != 1 {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
}

func TestFloodAreasEmptyCollectionIsValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
	}))
	defer srv.Close()
	snap, err := newClient(srv.URL).FloodAreas(context.Background(), domain.Period7D)
	if err != nil || len(snap.Areas) != 0 || snap.ObservedAt != nil {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
}

func isUnavailable(err error) bool {
	var ae *apperr.Error
	return errors.As(err, &ae) && ae.Code == apperr.CodeUnavailable
}

func TestFloodAreasInvalidUpstreamResponse(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     `<html>maintenance</html>`,
		"not a FC":     `{"status":"ok"}`,
		"all rejected": `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Write([]byte(body))
			}))
			defer srv.Close()
			_, err := newClient(srv.URL).FloodAreas(context.Background(), domain.Period1D)
			if !isUnavailable(err) {
				t.Fatalf("err = %v", err)
			}
			if calls.Load() != 1 {
				t.Errorf("a malformed body is not retried; calls = %d", calls.Load())
			}
		})
	}
}

func TestFloodAreasRetriesTransientFailureOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := newClient(srv.URL).FloodAreas(context.Background(), domain.Period1D)
	if !isUnavailable(err) || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d (want exactly one retry)", err, calls.Load())
	}
}

func TestFloodAreasDoesNotRetryAuthFailureOrLeakKey(t *testing.T) {
	logs := captureLog(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"INVALID_API_KEY","detail":"API Key is invalid"}`))
	}))
	defer srv.Close()
	_, err := newClient(srv.URL).FloodAreas(context.Background(), domain.Period1D)
	if !isUnavailable(err) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(logs.String(), testKey) {
		t.Error("API key leaked into an error or the log")
	}
	if !strings.Contains(logs.String(), "status=401") {
		t.Errorf("upstream status not logged: %s", logs.String())
	}
}

func TestFloodAreasTimeout(t *testing.T) {
	logs := captureLog(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := New(srv.URL, testKey, 50*time.Millisecond, fixedClock{now})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second) // cuts the retry wait short
	defer cancel()
	start := time.Now()
	_, err := c.FloodAreas(ctx, domain.Period1D)
	if !isUnavailable(err) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v; the timeout didn't bound the call", time.Since(start))
	}
	if !strings.Contains(logs.String(), "timeout") || strings.Contains(logs.String(), testKey) {
		t.Errorf("log = %s", logs.String())
	}
}

func TestKeyNotForwardedOnCrossHostRedirect(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("API-Key") != "" {
			leaked.Store(true)
		}
		w.Write([]byte(oneFeature))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()
	_, _ = newClient(srv.URL).FloodAreas(context.Background(), domain.Period1D)
	if leaked.Load() {
		t.Fatal("API-Key was sent to another host")
	}
}
