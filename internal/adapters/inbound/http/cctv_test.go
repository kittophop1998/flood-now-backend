package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"floodnow-api/internal/adapters/outbound/dohtraffic"
	appcctv "floodnow-api/internal/application/cctv"
	domain "floodnow-api/internal/domain/cctv"
)

// fakeHighwayTraffic serves a trimmed copy of DOH's public map page: two
// stations in Bangkok and one in Chiang Mai.
func fakeHighwayTraffic(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`<table>
<tr><td><a href='javascript:MoveLocation2(11)'>PER-1-001</a></td><td>1</td><td>0401</td><td><span id="km">10+000</span></td></tr>
<tr><td><a href='javascript:MoveLocation2(12)'>PER-1-002</a></td><td>2</td><td>0101</td><td><span id="km">5+500</span></td></tr>
</table><script>
CreateCustomDiv(13.7563, 100.5018, 'PER-1-001', '<div id="pin11" onclick="MoveLocation(11);" class="pin7"></div>');
CreateCustomDiv(13.7600, 100.5018, 'PER-1-002', '<div id="pin12" onclick="MoveLocation(12);" class="pin12"></div>');
CreateCustomDiv(18.7883, 98.9853, 'PER-9-009', '<div id="pin99" onclick="MoveLocation(99);" class="pin8"></div>');
</script>`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newCCTVRouter(service *appcctv.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{
		ConfigHandler: NewConfigHandler(nil, false, service != nil),
		CCTVHandler:   NewCCTVHandler(service),
		WebOrigin:     "http://localhost:3000",
	})
}

type cctvBody struct {
	Source     string           `json:"source"`
	SourceName string           `json:"source_name"`
	SourceURL  string           `json:"source_url"`
	FetchedAt  string           `json:"fetched_at"`
	Stale      bool             `json:"stale"`
	HasMore    bool             `json:"has_more"`
	Cameras    []map[string]any `json:"cameras"`
	Camera     map[string]any   `json:"camera"`
}

func decode(t *testing.T, w *httptest.ResponseRecorder) cctvBody {
	t.Helper()
	var b cctvBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	return b
}

func TestCCTVEndpoints(t *testing.T) {
	srv := fakeHighwayTraffic(t, http.StatusOK)
	r := newCCTVRouter(appcctv.NewService(dohtraffic.New(srv.URL, 2*time.Second, testClock{}), testClock{}, time.Hour))

	w := get(r, "/api/v1/cctv?min_lat=13&max_lat=14&min_lng=100&max_lng=101")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	// Nothing of the provider's page/schema reaches clients.
	for _, leak := range []string{"MoveLocation", "pin12", "CreateCustomDiv", srv.URL, "site_code"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("response leaks %q: %s", leak, w.Body)
		}
	}
	b := decode(t, w)
	if b.Source != "DOH" || b.SourceName != domain.SourceName || b.SourceURL != domain.SourceURL || b.Stale || b.FetchedAt != "2026-09-26T03:00:00Z" {
		t.Errorf("envelope = %+v", b)
	}
	if len(b.Cameras) != 2 {
		t.Fatalf("cameras = %v", b.Cameras)
	}
	want := map[string]any{
		"id": "doh-11", "external_id": "11", "name": "PER-1-001", "latitude": 13.7563, "longitude": 100.5018,
		"provider": "DOH", "highway_number": "1", "control_section": "0401", "km_marker": "10+000",
		"mode": "external_link", "external_url": domain.SourceURL, "status": "unknown",
	}
	if c := b.Cameras[0]; len(c) != len(want) {
		t.Errorf("camera fields = %v", c)
	} else {
		for k, v := range want {
			if c[k] != v {
				t.Errorf("%s = %v, want %v", k, c[k], v)
			}
		}
	}

	if b := decode(t, get(r, "/api/v1/cctv?limit=1")); len(b.Cameras) != 1 || !b.HasMore {
		t.Errorf("limit: %+v", b)
	}
	for _, q := range []string{"limit=5000", "min_lat=13", "min_lat=14&max_lat=13&min_lng=100&max_lng=101"} {
		if w := get(r, "/api/v1/cctv?"+q); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, w.Code)
		}
	}

	b = decode(t, get(r, "/api/v1/cctv/nearby?lat=13.7563&lng=100.5018&radius_m=1000"))
	if len(b.Cameras) != 2 || b.Cameras[0]["id"] != "doh-11" || b.Cameras[0]["distance_m"] != float64(0) || b.Cameras[1]["distance_m"] != float64(411) {
		t.Errorf("nearby = %v", b.Cameras)
	}
	if w := get(r, "/api/v1/cctv/nearby?lat=13.7"); w.Code != http.StatusBadRequest {
		t.Errorf("nearby without lng: status %d", w.Code)
	}

	b = decode(t, get(r, "/api/v1/cctv/doh-99"))
	if b.Camera["name"] != "PER-9-009" || b.Camera["highway_number"] != nil || b.Source != "DOH" {
		t.Errorf("get = %+v", b)
	}
	if w := get(r, "/api/v1/cctv/doh-404"); w.Code != http.StatusNotFound {
		t.Errorf("unknown camera: status %d", w.Code)
	}

	if w := get(r, "/api/v1/config/public"); !strings.Contains(w.Body.String(), `"doh_cctv":true`) {
		t.Errorf("public config = %s", w.Body)
	}
}

func TestCCTVEndpointsDisabled(t *testing.T) {
	r := newCCTVRouter(nil)
	for _, path := range []string{"/api/v1/cctv", "/api/v1/cctv/nearby?lat=13&lng=100", "/api/v1/cctv/doh-1"} {
		if w := get(r, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, w.Code)
		}
	}
	if w := get(r, "/api/v1/config/public"); !strings.Contains(w.Body.String(), `"doh_cctv":false`) {
		t.Errorf("public config = %s", w.Body)
	}
}

func TestCCTVEndpointUpstreamDown(t *testing.T) {
	srv := fakeHighwayTraffic(t, http.StatusServiceUnavailable)
	r := newCCTVRouter(appcctv.NewService(dohtraffic.New(srv.URL, time.Second, testClock{}), testClock{}, time.Hour))
	w := get(r, "/api/v1/cctv")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "UPSTREAM_UNAVAILABLE") {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
}
