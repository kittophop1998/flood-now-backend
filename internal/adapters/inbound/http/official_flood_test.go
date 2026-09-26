package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"floodnow-api/internal/adapters/outbound/gistda"
	appflood "floodnow-api/internal/application/officialflood"
	domain "floodnow-api/internal/domain/officialflood"
)

const secretKey = "never-send-me-to-clients-123"

type testClock struct{}

func (testClock) Now() time.Time { return time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC) }

// fakeGISTDA stands in for the GISTDA gateway: it checks the key and serves
// one flood polygon over Bangkok with a few extra properties that must not
// be passed through.
func fakeGISTDA(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("API-Key") != secretKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"type":"FeatureCollection","features":[{"type":"Feature","id":7,
		  "geometry":{"type":"Polygon","coordinates":[[[100.5,13.7],[100.6,13.7],[100.6,13.8],[100.5,13.7]]]},
		  "properties":{"f_date":"2026-09-25","tambon":"x","api_key_hint":"internal"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newFloodRouter(service *appflood.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{
		ConfigHandler:        NewConfigHandler(nil, service != nil, false),
		OfficialFloodHandler: NewOfficialFloodHandler(service),
		WebOrigin:            "http://localhost:3000",
	})
}

func get(r http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w
}

func TestOfficialFloodEndpoint(t *testing.T) {
	srv := fakeGISTDA(t)
	service := appflood.NewService(gistda.New(srv.URL, secretKey, 2*time.Second, testClock{}), testClock{}, 15*time.Minute)
	r := newFloodRouter(service)

	w := get(r, "/api/v1/official/gistda/flood?min_lat=13&max_lat=14&min_lng=100&max_lng=101")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), secretKey) || strings.Contains(w.Body.String(), "tambon") || strings.Contains(w.Body.String(), "api_key_hint") {
		t.Fatalf("response leaks the key or raw provider properties: %s", w.Body)
	}
	var body struct {
		Source     string  `json:"source"`
		Period     string  `json:"period"`
		ObservedAt *string `json:"observed_at"`
		FetchedAt  string  `json:"fetched_at"`
		Stale      bool    `json:"stale"`
		SourceURL  string  `json:"source_url"`
		Areas      struct {
			Type     string `json:"type"`
			Features []struct {
				Geometry struct {
					Type string `json:"type"`
				} `json:"geometry"`
				Properties map[string]any `json:"properties"`
			} `json:"features"`
		} `json:"areas"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "GISTDA" || body.Period != "1d" || body.Stale || body.SourceURL != domain.SourceURL || body.FetchedAt == "" {
		t.Errorf("envelope = %+v", body)
	}
	if body.ObservedAt == nil || *body.ObservedAt != "2026-09-24T17:00:00Z" {
		t.Errorf("observed_at = %v", body.ObservedAt)
	}
	if body.Areas.Type != "FeatureCollection" || len(body.Areas.Features) != 1 || body.Areas.Features[0].Geometry.Type != "MultiPolygon" {
		t.Fatalf("areas = %+v", body.Areas)
	}
	if p := body.Areas.Features[0].Properties; p["id"] != "7" || p["ref"] != float64(0) || len(p) != 3 {
		t.Errorf("properties = %v", p)
	}

	// Outside the viewport: nothing.
	w = get(r, "/api/v1/official/gistda/flood?period=30d&min_lat=18&max_lat=19&min_lng=98&max_lng=99")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"features":[]`) {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}

	if w = get(r, "/api/v1/official/gistda/flood?period=2d"); w.Code != http.StatusBadRequest {
		t.Errorf("bad period: status %d", w.Code)
	}

	w = get(r, "/api/v1/config/public")
	if !strings.Contains(w.Body.String(), `"gistda_flood":true`) || strings.Contains(w.Body.String(), secretKey) {
		t.Errorf("public config = %s", w.Body)
	}
}

func TestOfficialFloodEndpointDisabled(t *testing.T) {
	r := newFloodRouter(nil)
	if w := get(r, "/api/v1/official/gistda/flood"); w.Code != http.StatusNotFound {
		t.Errorf("status %d", w.Code)
	}
	if w := get(r, "/api/v1/config/public"); !strings.Contains(w.Body.String(), `"gistda_flood":false`) {
		t.Errorf("public config = %s", w.Body)
	}
}

func TestOfficialFloodEndpointUpstreamDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	service := appflood.NewService(gistda.New(srv.URL, "wrong", time.Second, testClock{}), testClock{}, 15*time.Minute)
	w := get(newFloodRouter(service), "/api/v1/official/gistda/flood")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "UPSTREAM_UNAVAILABLE") {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
}
