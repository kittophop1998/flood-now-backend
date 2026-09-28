package http

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appauth "floodnow-api/internal/application/auth"
	appevent "floodnow-api/internal/application/event"
	appfollow "floodnow-api/internal/application/follow"
	appreport "floodnow-api/internal/application/report"
	appsos "floodnow-api/internal/application/sos"
	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/event"
	"floodnow-api/internal/domain/follow"
	"floodnow-api/internal/domain/report"
	"floodnow-api/internal/domain/user"
	"floodnow-api/internal/ports"
)

// The guest/user permission matrix, exercised through the real router,
// middleware and services over in-memory repositories.

type accessUsers struct {
	users    map[uuid.UUID]user.User
	sessions map[string]uuid.UUID
}

func (m *accessUsers) Create(_ context.Context, u *user.User) error {
	for _, e := range m.users {
		if e.Email == u.Email {
			return apperr.Conflict("taken")
		}
	}
	m.users[u.ID] = *u
	return nil
}
func (m *accessUsers) GetByEmail(_ context.Context, email string) (*user.User, error) {
	for _, u := range m.users {
		if u.Email == email {
			return &u, nil
		}
	}
	return nil, nil
}
func (m *accessUsers) CreateSession(_ context.Context, hash string, id uuid.UUID, _, _ time.Time) error {
	m.sessions[hash] = id
	return nil
}
func (m *accessUsers) UserBySession(_ context.Context, hash string, _ time.Time) (*user.User, error) {
	id, ok := m.sessions[hash]
	if !ok {
		return nil, nil
	}
	u := m.users[id]
	return &u, nil
}
func (m *accessUsers) DeleteSession(_ context.Context, hash string) error {
	delete(m.sessions, hash)
	return nil
}

type accessHasher struct{}

func (accessHasher) Hash(p string) (string, error) { return "h:" + p, nil }
func (accessHasher) Compare(h, p string) bool      { return h == "h:"+p }

type accessReports struct {
	ports.ReportRepository
	items     map[uuid.UUID]report.ReportWithStats
	reactions map[uuid.UUID]report.ReactionType // by user, single report
}

func (m *accessReports) Create(_ context.Context, r *report.Report) error {
	m.items[r.ID] = report.ReportWithStats{Report: *r}
	return nil
}
func (m *accessReports) GetByID(_ context.Context, id uuid.UUID) (*report.ReportWithStats, error) {
	if r, ok := m.items[id]; ok {
		return &r, nil
	}
	return nil, nil
}
func (m *accessReports) React(_ context.Context, id, userID uuid.UUID, t report.ReactionType) (*report.ReportWithStats, error) {
	m.reactions[userID] = t
	r := m.items[id]
	r.LikeCount = len(m.reactions)
	return &r, nil
}
func (m *accessReports) MyReaction(_ context.Context, _, userID uuid.UUID) (*report.ReactionType, error) {
	if t, ok := m.reactions[userID]; ok {
		return &t, nil
	}
	return nil, nil
}

type accessFollows struct {
	ports.FollowRepository
	places []follow.Follow
}

func (m *accessFollows) CountPlaces(_ context.Context, userID uuid.UUID) (int, error) {
	n := 0
	for _, p := range m.places {
		if *p.UserID == userID {
			n++
		}
	}
	return n, nil
}
func (m *accessFollows) Create(_ context.Context, f *follow.Follow) error {
	m.places = append(m.places, *f)
	return nil
}
func (m *accessFollows) PlaceSummaries(_ context.Context, userID uuid.UUID, _ []report.Severity, _ []report.Type, _ time.Time) ([]follow.PlaceWithSummary, error) {
	out := []follow.PlaceWithSummary{}
	for _, p := range m.places {
		if *p.UserID == userID {
			out = append(out, follow.PlaceWithSummary{Follow: p})
		}
	}
	return out, nil
}

type accessEvents struct{ items map[uuid.UUID]event.Event }

func (m *accessEvents) List(_ context.Context, f ports.EventFilter) ([]event.Event, error) {
	out := []event.Event{}
	for _, e := range m.items {
		if e.EndAt.After(f.Now) {
			out = append(out, e)
		}
	}
	return out, nil
}
func (m *accessEvents) ListByOwner(context.Context, uuid.UUID, int) ([]event.Event, error) {
	return nil, nil
}
func (m *accessEvents) CountUpcomingByOwner(context.Context, uuid.UUID, time.Time) (int, error) {
	return 0, nil
}
func (m *accessEvents) Get(_ context.Context, id uuid.UUID) (*event.Event, error) {
	if e, ok := m.items[id]; ok {
		return &e, nil
	}
	return nil, nil
}
func (m *accessEvents) Create(_ context.Context, e *event.Event) error {
	e.OrganizerName = "สมชาย"
	m.items[e.ID] = *e
	return nil
}
func (m *accessEvents) Update(_ context.Context, e *event.Event) error {
	m.items[e.ID] = *e
	return nil
}
func (m *accessEvents) Delete(_ context.Context, id uuid.UUID) error { delete(m.items, id); return nil }

type accessFixture struct {
	router  *gin.Engine
	reports *accessReports
	events  *accessEvents
	follows *accessFollows
}

func newAccessFixture() *accessFixture {
	gin.SetMode(gin.TestMode)
	clock := testClock{}
	f := &accessFixture{
		reports: &accessReports{items: map[uuid.UUID]report.ReportWithStats{}, reactions: map[uuid.UUID]report.ReactionType{}},
		events:  &accessEvents{items: map[uuid.UUID]event.Event{}},
		follows: &accessFollows{},
	}
	users := &accessUsers{users: map[uuid.UUID]user.User{}, sessions: map[string]uuid.UUID{}}
	authService := appauth.NewService(users, f.follows, accessHasher{}, clock)
	imageURL := func(base, key string) string { return base + "/" + key }
	followService := appfollow.NewService(f.follows, f.reports, clock)
	policy := report.FreshnessPolicy{StaleAfter: 2 * time.Hour, TTL: 6 * time.Hour, FacilityStaleAfter: 12 * time.Hour, FacilityTTL: 48 * time.Hour, ResolveThreshold: 2}
	f.router = NewRouter(Deps{
		ReportHandler:     NewReportHandler(appreport.NewService(f.reports, clock, policy), NewReportPresenter(clock, "https://ik.example", imageURL)),
		SavedPlaceHandler: NewSavedPlaceHandler(followService),
		SOSHandler:        NewSOSHandler(appsos.NewService(nil, clock)),
		AuthHandler:       NewAuthHandler(authService),
		EventHandler:      NewEventHandler(appevent.NewService(f.events, clock), clock, "https://ik.example", imageURL),
		AuthService:       authService,
		WebOrigin:         "http://localhost:3000",
	})
	return f
}

// signUp registers an account and returns its bearer token.
func (f *accessFixture) signUp(t *testing.T, email string) string {
	t.Helper()
	w := send(f.router, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"email": email, "password": "correct horse", "display_name": "สมชาย",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	return decodeJSON[sessionResponse](t, w).Token
}

func (f *accessFixture) seedReport() uuid.UUID {
	id := uuid.New()
	now := testClock{}.Now()
	f.reports.items[id] = report.ReportWithStats{Report: report.Report{
		ID: id, Type: report.TypeFlooded, Severity: report.SeverityHigh, Latitude: 13.75, Longitude: 100.5,
		CreatedAt: now, UpdatedAt: now, LastVerifiedAt: now, StaleAt: now.Add(2 * time.Hour), ExpiresAt: now.Add(6 * time.Hour),
	}}
	return id
}

func eventBody() map[string]any {
	return map[string]any{
		"title": "ตลาดนัดถนนคนเดิน", "category": "walking_street", "latitude": 13.75, "longitude": 100.5,
		"start_at": "2026-09-27T10:00:00Z", "end_at": "2026-09-27T22:00:00Z",
	}
}

func assertStatus(t *testing.T, w interface {
	Result() *http.Response
}, want int, what string) {
	t.Helper()
	if got := w.Result().StatusCode; got != want {
		t.Errorf("%s: status %d, want %d", what, got, want)
	}
}

func TestGuestPermissions(t *testing.T) {
	f := newAccessFixture()
	reportID := f.seedReport()

	// Viewing is open to everyone.
	assertStatus(t, get(f.router, "/api/v1/reports/"+reportID.String()), http.StatusOK, "guest views report")
	assertStatus(t, get(f.router, "/api/v1/events"), http.StatusOK, "guest lists events")

	// A safety-critical incident can be reported anonymously…
	w := send(f.router, http.MethodPost, "/api/v1/reports", "", map[string]any{
		"type": "accident", "severity": "high", "latitude": 13.75, "longitude": 100.5,
	})
	assertStatus(t, w, http.StatusCreated, "guest reports an accident")
	// …a non-guest category can't, whatever the client sends.
	w = send(f.router, http.MethodPost, "/api/v1/reports", "", map[string]any{
		"type": "construction", "severity": "moderate", "latitude": 13.75, "longitude": 100.5,
	})
	assertStatus(t, w, http.StatusUnauthorized, "guest reports construction")

	// Interactions, private data and owned content need an account.
	for _, c := range []struct{ method, path, what string }{
		{http.MethodPost, "/api/v1/reports/" + reportID.String() + "/reactions", "react"},
		{http.MethodDelete, "/api/v1/reports/" + reportID.String() + "/reactions", "unreact"},
		{http.MethodPost, "/api/v1/sos", "send SOS"},
		{http.MethodPost, "/api/v1/sos/" + uuid.NewString() + "/accept", "accept SOS"},
		{http.MethodPut, "/api/v1/helpers/me", "helper profile"},
		{http.MethodGet, "/api/v1/saved-places", "list saved places"},
		{http.MethodPost, "/api/v1/saved-places", "save a place"},
		{http.MethodPost, "/api/v1/events", "create event"},
		{http.MethodPatch, "/api/v1/events/" + uuid.NewString(), "edit event"},
		{http.MethodGet, "/api/v1/events/mine", "my events"},
	} {
		w := send(f.router, c.method, c.path, "", map[string]any{"type": "like", "device_id": "12345678-aaaa-bbbb-cccc-dddddddddddd"})
		assertStatus(t, w, http.StatusUnauthorized, "guest "+c.what)
		if !strings.Contains(w.Body.String(), `"UNAUTHORIZED"`) {
			t.Errorf("guest %s: body %s", c.what, w.Body)
		}
	}
	// A forged token is no better than none.
	w = send(f.router, http.MethodPost, "/api/v1/events", "forged-token", eventBody())
	assertStatus(t, w, http.StatusUnauthorized, "forged token creates event")
}

func TestSignedInUserPermissions(t *testing.T) {
	f := newAccessFixture()
	reportID := f.seedReport()
	alice := f.signUp(t, "alice@example.com")
	bob := f.signUp(t, "bob@example.com")

	w := send(f.router, http.MethodPost, "/api/v1/reports/"+reportID.String()+"/reactions", alice, map[string]any{"type": "support"})
	assertStatus(t, w, http.StatusOK, "user reacts")
	if got := decodeJSON[reportWithReactionResponse](t, w); got.MyReaction == nil || *got.MyReaction != "support" {
		t.Errorf("my_reaction = %v, want support", got.MyReaction)
	}
	w = send(f.router, http.MethodGet, "/api/v1/reports/"+reportID.String(), bob, nil)
	if got := decodeJSON[reportWithReactionResponse](t, w); got.MyReaction != nil {
		t.Errorf("bob sees alice's reaction as his own: %v", *got.MyReaction)
	}

	w = send(f.router, http.MethodPost, "/api/v1/reports", alice, map[string]any{
		"type": "construction", "severity": "moderate", "latitude": 13.75, "longitude": 100.5,
	})
	assertStatus(t, w, http.StatusCreated, "user reports construction")

	// Saved places are private to their owner.
	w = send(f.router, http.MethodPost, "/api/v1/saved-places", alice, map[string]any{
		"name": "ที่ทำงาน", "icon": "work", "latitude": 13.72, "longitude": 100.53,
	})
	assertStatus(t, w, http.StatusCreated, "user saves a place")
	w = send(f.router, http.MethodGet, "/api/v1/saved-places", bob, nil)
	if strings.Contains(w.Body.String(), "ที่ทำงาน") {
		t.Errorf("bob can read alice's saved place: %s", w.Body)
	}

	// Events: create, public read without owner identity, owner-only edits.
	w = send(f.router, http.MethodPost, "/api/v1/events", alice, eventBody())
	assertStatus(t, w, http.StatusCreated, "user creates event")
	created := decodeJSON[eventResponse](t, w)
	if !created.IsMine || created.Organizer.DisplayName != "สมชาย" || created.Status != "active" {
		t.Errorf("unexpected event response: %+v", created)
	}

	w = get(f.router, "/api/v1/events/"+created.ID)
	assertStatus(t, w, http.StatusOK, "guest views event")
	body := w.Body.String()
	for _, leak := range []string{"alice@example.com", "owner_user_id", "password"} {
		if strings.Contains(body, leak) {
			t.Errorf("public event leaks %q: %s", leak, body)
		}
	}
	if decodeJSON[eventResponse](t, w).IsMine {
		t.Error("is_mine must be false for a guest")
	}

	w = send(f.router, http.MethodPatch, "/api/v1/events/"+created.ID, bob, map[string]any{"title": "hijacked"})
	assertStatus(t, w, http.StatusForbidden, "another user edits event")
	w = send(f.router, http.MethodPost, "/api/v1/events/"+created.ID+"/cancel", bob, nil)
	assertStatus(t, w, http.StatusForbidden, "another user cancels event")

	w = send(f.router, http.MethodPatch, "/api/v1/events/"+created.ID, alice, map[string]any{"title": "ถนนคนเดินวันเสาร์"})
	assertStatus(t, w, http.StatusOK, "owner edits event")
	w = send(f.router, http.MethodPost, "/api/v1/events/"+created.ID+"/cancel", alice, nil)
	assertStatus(t, w, http.StatusOK, "owner cancels event")
	if got := decodeJSON[eventResponse](t, w); got.Status != "cancelled" || got.Title != "ถนนคนเดินวันเสาร์" {
		t.Errorf("after edit+cancel: %+v", got)
	}

	// Signing out ends the session.
	assertStatus(t, send(f.router, http.MethodPost, "/api/v1/auth/logout", alice, nil), http.StatusNoContent, "logout")
	assertStatus(t, send(f.router, http.MethodGet, "/api/v1/auth/me", alice, nil), http.StatusUnauthorized, "me after logout")
}

func TestAuthMeNeverExposesThePasswordHash(t *testing.T) {
	f := newAccessFixture()
	token := f.signUp(t, "carol@example.com")
	w := send(f.router, http.MethodGet, "/api/v1/auth/me", token, nil)
	assertStatus(t, w, http.StatusOK, "me")
	if strings.Contains(w.Body.String(), "h:correct horse") || strings.Contains(w.Body.String(), "password") {
		t.Errorf("/auth/me leaks credentials: %s", w.Body)
	}
}
