package dohtraffic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"floodnow-api/internal/domain/apperr"
	domain "floodnow-api/internal/domain/cctv"
)

type testClock struct{}

func (testClock) Now() time.Time { return time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC) }

// fixturePage mirrors the shape of the public Home.aspx: inline map pins and
// the station table. It includes a pin with no table row, a 0,0 pin, a
// duplicate site id and an entity-escaped highway number.
const fixturePage = `<html><body>
<table id="listGridView">
<tr class="data-row"><td>
    <a href='javascript:MoveLocation2(2753)'>PER-3-003</a>
</td><td>1</td><td>0401</td><td>
    <span id="listGridView_kmLabel_0">91+900</span>
</td></tr>
<tr class="alt-data-row"><td>
    <a href='javascript:MoveLocation2(3846)'>Tmp-080</a>
</td><td>&#3585;&#3607;.1001</td><td>0100</td><td>
    <span id="listGridView_kmLabel_1">48+300</span>
</td></tr>
</table>
<script>
init();CreateCustomDiv(14.3914, 100.8880, 'PER-3-003', '<div id="pin2753" onclick="MoveLocation(2753);" class="pin7"></div>');CreateCustomDiv(13.8496, 99.9771, 'Tmp-080', '<div id="pin3846" onclick="MoveLocation(3846);" class="pin12"></div>');CreateCustomDiv(16.9381, 101.2192, 'PER-7-003', '<div id="pin3097" onclick="MoveLocation(3097);" class="pin8"></div>');CreateCustomDiv(0, 0, 'BAD-1', '<div id="pin1" onclick="MoveLocation(1);" class="pin7"></div>');CreateCustomDiv(14.0, 100.0, 'PER-3-003', '<div id="pin2753" onclick="MoveLocation(2753);" class="pin7"></div>');
</script></body></html>`

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestCamerasNormalizesThePublicPage(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/DOHWeb/Home.aspx" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("request must be anonymous")
		}
		w.Write([]byte(fixturePage))
	})
	cat, err := New(srv.URL+"/", 2*time.Second, testClock{}).Cameras(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Cameras) != 3 || !cat.FetchedAt.Equal(testClock{}.Now()) {
		t.Fatalf("catalog = %+v", cat)
	}
	want := domain.Camera{
		ID: "doh-2753", ExternalID: "2753", Name: "PER-3-003", Latitude: 14.3914, Longitude: 100.888,
		Provider: "DOH", HighwayNumber: "1", ControlSection: "0401", KMMarker: "91+900",
		Mode: domain.ModeExternalLink, ExternalURL: domain.SourceURL, Status: domain.StatusUnknown,
	}
	if cat.Cameras[0] != want {
		t.Errorf("camera[0] = %+v\nwant %+v", cat.Cameras[0], want)
	}
	if got := cat.Cameras[1].HighwayNumber; got != "กท.1001" {
		t.Errorf("entity-escaped highway = %q", got)
	}
	// A pin without a table row keeps its position, without road details.
	if c := cat.Cameras[2]; c.ID != "doh-3097" || c.HighwayNumber != "" || c.KMMarker != "" {
		t.Errorf("camera without row = %+v", c)
	}
	// The external link is always DOH's public page, never the fetch URL.
	for _, c := range cat.Cameras {
		if c.ExternalURL != domain.SourceURL {
			t.Errorf("external url = %q", c.ExternalURL)
		}
	}
}

func TestMalformedPageIsUnavailable(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>maintenance</body></html>"))
	})
	_, err := New(srv.URL, 2*time.Second, testClock{}).Cameras(context.Background())
	if !isUnavailable(err) {
		t.Fatalf("err = %v, want UNAVAILABLE", err)
	}
}

func TestRetriesOnlyTransientFailuresOnce(t *testing.T) {
	var calls atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := New(srv.URL, 2*time.Second, testClock{}).Cameras(context.Background())
	if !isUnavailable(err) || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d, want UNAVAILABLE after 2 calls", err, calls.Load())
	}

	calls.Store(0)
	srv404 := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	})
	_, err = New(srv404.URL, 2*time.Second, testClock{}).Cameras(context.Background())
	if !isUnavailable(err) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d, want no retry on 403", err, calls.Load())
	}
}

func isUnavailable(err error) bool {
	var ae *apperr.Error
	return errors.As(err, &ae) && ae.Code == apperr.CodeUnavailable
}
