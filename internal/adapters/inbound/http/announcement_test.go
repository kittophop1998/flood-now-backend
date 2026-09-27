package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appannouncement "floodnow-api/internal/application/announcement"
	appupload "floodnow-api/internal/application/upload"
	"floodnow-api/internal/domain/announcement"
	"floodnow-api/internal/ports"
)

const testAdminToken = "test-admin-token-at-least-24-chars"

// memAnnouncements is an in-memory AnnouncementRepository honoring the
// draft filter (enough for handler-level behavior).
type memAnnouncements struct {
	items map[uuid.UUID]announcement.Announcement
}

func (m *memAnnouncements) List(_ context.Context, f ports.AnnouncementFilter) ([]announcement.Announcement, error) {
	out := []announcement.Announcement{}
	for _, a := range m.items {
		if f.IncludeDrafts || a.PublishedAt != nil {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memAnnouncements) Get(_ context.Context, id uuid.UUID) (*announcement.Announcement, error) {
	if a, ok := m.items[id]; ok {
		return &a, nil
	}
	return nil, nil
}
func (m *memAnnouncements) Create(_ context.Context, a *announcement.Announcement) error {
	m.items[a.ID] = *a
	return nil
}
func (m *memAnnouncements) Update(_ context.Context, a *announcement.Announcement) error {
	m.items[a.ID] = *a
	return nil
}
func (m *memAnnouncements) Delete(_ context.Context, id uuid.UUID) (bool, error) {
	_, ok := m.items[id]
	delete(m.items, id)
	return ok, nil
}

type fakePresigner struct{ keys []string }

func (p *fakePresigner) PresignUpload(_ context.Context, key, _ string, _ int64) (string, time.Duration, error) {
	p.keys = append(p.keys, key)
	return "https://r2.example/" + key + "?sig=x", 5 * time.Minute, nil
}

func newAnnouncementRouter(repo *memAnnouncements, presigner *fakePresigner) *gin.Engine {
	gin.SetMode(gin.TestMode)
	clock := testClock{}
	return NewRouter(Deps{
		AnnouncementHandler: NewAnnouncementHandler(appannouncement.NewService(repo, clock), clock, "https://ik.example/fn", func(base, key string) string {
			return base + "/" + key
		}),
		UploadHandler: NewUploadHandler(appupload.NewService(presigner, clock)),
		AdminToken:    testAdminToken,
		WebOrigin:     "http://localhost:3000",
	})
}

func send(r http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func validAnnouncement() map[string]any {
	return map[string]any{
		"title": "ปิดการจราจรชั่วคราว ถนนพระราม 4", "body": "ปิดช่องทางซ้ายเพื่อซ่อมท่อ", "type": "traffic_notice",
		"severity": "info", "source_name": "กรุงเทพมหานคร", "source_url": "https://bangkok.go.th/notice",
		"latitude": 13.72, "longitude": 100.55, "radius_m": 500, "starts_at": "2026-09-26T01:00:00Z",
	}
}

func decodeJSON[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

func TestAdminAnnouncementEndpointsRequireToken(t *testing.T) {
	r := newAnnouncementRouter(&memAnnouncements{items: map[uuid.UUID]announcement.Announcement{}}, &fakePresigner{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/announcements"},
		{http.MethodPost, "/api/v1/admin/uploads/presign"},
		{http.MethodPatch, "/api/v1/admin/announcements/" + uuid.NewString()},
		{http.MethodPost, "/api/v1/admin/announcements/" + uuid.NewString() + "/publish"},
		{http.MethodDelete, "/api/v1/admin/announcements/" + uuid.NewString()},
	} {
		if w := send(r, tc.method, tc.path, "wrong-token", validAnnouncement()); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a bad token = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

func TestAdminPresignMintsAnnouncementKeys(t *testing.T) {
	presigner := &fakePresigner{}
	r := newAnnouncementRouter(&memAnnouncements{items: map[uuid.UUID]announcement.Announcement{}}, presigner)
	w := send(r, http.MethodPost, "/api/v1/admin/uploads/presign", testAdminToken, map[string]any{"content_type": "image/webp", "content_length": 1024})
	if w.Code != http.StatusOK {
		t.Fatalf("admin presign = %d %s", w.Code, w.Body.String())
	}
	if key := decodeJSON[presignResponse](t, w).ObjectKey; !strings.HasPrefix(key, "announcements/") || !strings.HasSuffix(key, ".webp") {
		t.Errorf("object key = %q, want announcements/….webp", key)
	}
	if w := send(r, http.MethodPost, "/api/v1/admin/uploads/presign", testAdminToken, map[string]any{"content_type": "application/pdf", "content_length": 1024}); w.Code != http.StatusBadRequest {
		t.Errorf("non-image presign = %d, want 400", w.Code)
	}
	if w := send(r, http.MethodPost, "/api/v1/admin/uploads/presign", testAdminToken, map[string]any{"content_type": "image/jpeg", "content_length": 9 << 20}); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized presign = %d, want 413", w.Code)
	}
	// The public endpoint keeps minting report keys.
	w = send(r, http.MethodPost, "/api/v1/uploads/presign", "", map[string]any{"content_type": "image/jpeg", "content_length": 1024})
	if key := decodeJSON[presignResponse](t, w).ObjectKey; !strings.HasPrefix(key, "reports/") {
		t.Errorf("public presign key = %q, want reports/…", key)
	}
}

func TestCreateAnnouncementWithImagesDraftAndPublish(t *testing.T) {
	repo := &memAnnouncements{items: map[uuid.UUID]announcement.Announcement{}}
	r := newAnnouncementRouter(repo, &fakePresigner{})

	body := validAnnouncement()
	body["images"] = []map[string]any{
		{"image_key": "announcements/2026/09/26/a.jpg", "width": 1600, "height": 900},
		{"image_key": "announcements/2026/09/26/b.jpg"},
	}
	w := send(r, http.MethodPost, "/api/v1/admin/announcements", testAdminToken, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	draft := decodeJSON[announcementResponse](t, w)
	if draft.Status != "draft" || draft.Type != "traffic_notice" || draft.Severity != "info" {
		t.Errorf("created %+v, want a draft info traffic_notice", draft)
	}
	if len(draft.Images) != 2 || draft.Images[0].ImageKey != "announcements/2026/09/26/a.jpg" ||
		draft.Images[0].ImageURL == nil || *draft.Images[0].ImageURL != "https://ik.example/fn/announcements/2026/09/26/a.jpg" ||
		draft.Images[0].Width == nil || *draft.Images[0].Width != 1600 || draft.Images[1].Width != nil {
		t.Errorf("images = %+v", draft.Images)
	}
	if w := send(r, http.MethodGet, "/api/v1/announcements/"+draft.ID, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("public get of a draft = %d, want 404", w.Code)
	}

	flood := validAnnouncement()
	flood["type"], flood["severity"], flood["publish"] = "flood_warning", "high", true
	w = send(r, http.MethodPost, "/api/v1/admin/announcements", testAdminToken, flood)
	published := decodeJSON[announcementResponse](t, w)
	if w.Code != http.StatusCreated || published.Status != "active" || published.Images == nil || len(published.Images) != 0 {
		t.Fatalf("published flood warning = %d %+v", w.Code, published)
	}
	if w := send(r, http.MethodGet, "/api/v1/announcements/"+published.ID, "", nil); w.Code != http.StatusOK {
		t.Errorf("public get of a published announcement = %d", w.Code)
	}

	// Clearing images with an empty list; omitting images leaves them alone.
	w = send(r, http.MethodPatch, "/api/v1/admin/announcements/"+draft.ID, testAdminToken, map[string]any{"title": "แก้ไขหัวข้อ"})
	if got := decodeJSON[announcementResponse](t, w); len(got.Images) != 2 {
		t.Errorf("patch without images dropped them: %+v", got.Images)
	}
	w = send(r, http.MethodPatch, "/api/v1/admin/announcements/"+draft.ID, testAdminToken, map[string]any{"images": []any{}})
	if got := decodeJSON[announcementResponse](t, w); len(got.Images) != 0 {
		t.Errorf("patch images: [] kept %+v", got.Images)
	}
}

func TestCreateAnnouncementValidation(t *testing.T) {
	r := newAnnouncementRouter(&memAnnouncements{items: map[uuid.UUID]announcement.Announcement{}}, &fakePresigner{})
	many := []map[string]any{}
	for i := 0; i < 6; i++ {
		many = append(many, map[string]any{"image_key": "announcements/x/" + string(rune('a'+i)) + ".jpg"})
	}
	cases := map[string]func(map[string]any){
		"missing title":    func(b map[string]any) { delete(b, "title") },
		"missing body":     func(b map[string]any) { b["body"] = "  " },
		"missing source":   func(b map[string]any) { delete(b, "source_name") },
		"unknown type":     func(b map[string]any) { b["type"] = "party" },
		"unknown severity": func(b map[string]any) { b["severity"] = "urgent" },
		"bad source url":   func(b map[string]any) { b["source_url"] = "ftp://example.com" },
		"bad latitude":     func(b map[string]any) { b["latitude"] = 123.0 },
		"zero radius":      func(b map[string]any) { b["radius_m"] = 0 },
		"end before start": func(b map[string]any) { b["ends_at"] = "2026-09-25T00:00:00Z" },
		"too many images":  func(b map[string]any) { b["images"] = many },
		"report image key": func(b map[string]any) { b["images"] = []map[string]any{{"image_key": "reports/2026/a.jpg"}} },
		"image url":        func(b map[string]any) { b["images"] = []map[string]any{{"image_key": "https://x.example/a.jpg"}} },
		"negative width": func(b map[string]any) {
			b["images"] = []map[string]any{{"image_key": "announcements/a.jpg", "width": -1}}
		},
	}
	for name, mutate := range cases {
		body := validAnnouncement()
		mutate(body)
		if w := send(r, http.MethodPost, "/api/v1/admin/announcements", testAdminToken, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: create = %d, want 400 (%s)", name, w.Code, w.Body.String())
		}
	}
	noURL := validAnnouncement()
	delete(noURL, "source_url")
	delete(noURL, "latitude")
	delete(noURL, "longitude")
	delete(noURL, "radius_m")
	noURL["type"], noURL["severity"] = "general", "info"
	if w := send(r, http.MethodPost, "/api/v1/admin/announcements", testAdminToken, noURL); w.Code != http.StatusCreated {
		t.Errorf("general notice without link/area = %d %s", w.Code, w.Body.String())
	}
}

// A record stored before images/new types existed (legacy type, no images)
// stays readable and editable.
func TestLegacyAnnouncementStaysReadableAndEditable(t *testing.T) {
	published := testClock{}.Now().Add(-time.Hour)
	legacy := announcement.Announcement{
		ID: uuid.New(), Title: "ระบายน้ำเขื่อน", Body: "เพิ่มการระบายน้ำ", Type: announcement.TypeWaterRelease, Severity: "moderate",
		SourceName: "กรมชลประทาน", StartsAt: published, PublishedAt: &published, CreatedAt: published, UpdatedAt: published,
	}
	repo := &memAnnouncements{items: map[uuid.UUID]announcement.Announcement{legacy.ID: legacy}}
	r := newAnnouncementRouter(repo, &fakePresigner{})

	w := send(r, http.MethodGet, "/api/v1/announcements/"+legacy.ID.String(), "", nil)
	got := decodeJSON[announcementResponse](t, w)
	if w.Code != http.StatusOK || got.Type != "water_release" || got.Images == nil || len(got.Images) != 0 {
		t.Fatalf("legacy read = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"images":[]`) {
		t.Errorf("images must serialize as [] for old rows: %s", w.Body.String())
	}
	w = send(r, http.MethodPatch, "/api/v1/admin/announcements/"+legacy.ID.String(), testAdminToken, map[string]any{
		"severity": "info", "images": []map[string]any{{"image_key": "announcements/2026/09/26/c.png"}},
	})
	got = decodeJSON[announcementResponse](t, w)
	if w.Code != http.StatusOK || got.Type != "water_release" || got.Severity != "info" || len(got.Images) != 1 {
		t.Errorf("legacy edit = %d %s", w.Code, w.Body.String())
	}
}
