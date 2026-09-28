package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"floodnow-api/internal/domain/user"
)

// Cookie sessions end to end through the real router: cookie attributes,
// CSRF + Origin checks on signed-in writes, CORS, logout/rotation/expiry
// and the production HTTPS guard.

const webOrigin = "http://localhost:3000"

var testCookie = SessionCookie{Name: "__Host-floodnow_session", Secure: true, SameSite: http.SameSiteStrictMode}

// browserSession is what a browser holds after signing in: the HttpOnly
// cookie (sent automatically) and the CSRF token the page keeps in memory.
type browserSession struct {
	cookie string
	csrf   string
}

var guest = browserSession{}

func sendAs(r http.Handler, s browserSession, method, path string, body any, decorate func(*http.Request)) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if s.cookie != "" {
		req.AddCookie(&http.Cookie{Name: testCookie.Name, Value: s.cookie})
	}
	if decorate != nil {
		decorate(req)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sessionCookieOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == testCookie.Name {
			return c
		}
	}
	return nil
}

func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) browserSession {
	t.Helper()
	c := sessionCookieOf(w)
	if c == nil || c.Value == "" {
		t.Fatalf("no session cookie set: %v", w.Header().Values("Set-Cookie"))
	}
	return browserSession{cookie: c.Value, csrf: decodeJSON[sessionResponse](t, w).CSRFToken}
}

func hashOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestLoginSetsASecureHttpOnlyCookieAndNoTokenInJSON(t *testing.T) {
	f := newAccessFixture()
	f.signUp(t, "dana@example.com")

	w := f.send(guest, http.MethodPost, "/api/v1/auth/login", map[string]any{"email": "dana@example.com", "password": "correct horse"})
	assertStatus(t, w, http.StatusOK, "login")
	c := sessionCookieOf(w)
	if c == nil {
		t.Fatal("login must set the session cookie")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
		t.Errorf("cookie attributes: %+v", c)
	}
	if c.MaxAge != int(user.SessionTTL.Seconds()) || !c.Expires.Equal(testClock{}.Now().Add(user.SessionTTL)) {
		t.Errorf("cookie Max-Age %d must match the session TTL", c.MaxAge)
	}
	if len(c.Value) < 43 { // 32 random bytes, base64url
		t.Errorf("session token too short: %d chars", len(c.Value))
	}
	if strings.Contains(w.Body.String(), c.Value) || strings.Contains(w.Body.String(), `"token"`) {
		t.Errorf("the raw session token must not be in the JSON body: %s", w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	body := decodeJSON[sessionResponse](t, w)
	if body.User.Email != "dana@example.com" || body.CSRFToken == "" || body.CSRFToken == c.Value {
		t.Errorf("unexpected session body: %+v", body)
	}
	// Only the token's hash is stored.
	if _, ok := f.users.sessions[hashOf(c.Value)]; !ok {
		t.Error("the session must be stored by its SHA-256")
	}
	if _, ok := f.users.sessions[c.Value]; ok {
		t.Error("the raw token must never be stored")
	}
}

func TestDevelopmentCookieCanBeInsecure(t *testing.T) {
	dev := SessionCookie{Name: "floodnow_session", SameSite: http.SameSiteStrictMode}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	dev.set(c, "tok", time.Now().Add(time.Hour), time.Hour)
	got := w.Result().Cookies()[0]
	if got.Secure || !got.HttpOnly || got.Name != "floodnow_session" {
		t.Errorf("dev cookie: %+v", got)
	}
}

func TestSessionEndpointForGuestsAndUsers(t *testing.T) {
	f := newAccessFixture()

	w := f.send(guest, http.MethodGet, "/api/v1/auth/session", nil)
	assertStatus(t, w, http.StatusOK, "guest session")
	if strings.TrimSpace(w.Body.String()) != `{"user":null,"csrf_token":null}` {
		t.Errorf("guest session body: %s", w.Body)
	}

	alice := f.signUp(t, "alice@example.com")
	w = f.send(alice, http.MethodGet, "/api/v1/auth/session", nil)
	got := decodeJSON[sessionStateResponse](t, w)
	if got.User == nil || got.User.Email != "alice@example.com" || got.CSRFToken == nil || *got.CSRFToken != alice.csrf {
		t.Errorf("signed-in session: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), alice.cookie) {
		t.Error("session endpoint must not echo the session token")
	}

	// A stale cookie reads as a guest and is cleared.
	w = f.send(browserSession{cookie: "stale"}, http.MethodGet, "/api/v1/auth/session", nil)
	if c := sessionCookieOf(w); c == nil || c.MaxAge >= 0 {
		t.Errorf("stale cookie should be cleared, got %v", w.Header().Values("Set-Cookie"))
	}
}

func TestInvalidExpiredAndRevokedSessionsAreRejected(t *testing.T) {
	f := newAccessFixture()
	alice := f.signUp(t, "alice@example.com")

	assertStatus(t, f.send(browserSession{cookie: "not-a-session"}, http.MethodGet, "/api/v1/auth/me", nil), http.StatusUnauthorized, "unknown session")

	// Expired: stored, but past its expiry.
	expired := "expired-session-token"
	f.users.sessions[hashOf(expired)] = accessSession{userID: f.users.sessions[hashOf(alice.cookie)].userID, expires: testClock{}.Now().Add(-time.Second)}
	assertStatus(t, f.send(browserSession{cookie: expired}, http.MethodGet, "/api/v1/auth/me", nil), http.StatusUnauthorized, "expired session")

	// A bearer header is not a way in any more: cookies only.
	w := sendAs(f.router, guest, http.MethodGet, "/api/v1/auth/me", nil, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+alice.cookie)
	})
	assertStatus(t, w, http.StatusUnauthorized, "bearer instead of cookie")

	// Revoked by logout.
	w = f.send(alice, http.MethodPost, "/api/v1/auth/logout", nil)
	assertStatus(t, w, http.StatusNoContent, "logout")
	if len(f.users.sessions) != 1 { // only the seeded expired one is left
		t.Errorf("logout must delete the server-side session, %d left", len(f.users.sessions))
	}
	c := sessionCookieOf(w)
	if c == nil || c.MaxAge >= 0 || c.Value != "" || !c.HttpOnly || !c.Secure || c.Path != "/" || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("logout must clear the cookie with matching attributes: %+v", c)
	}
	assertStatus(t, f.send(alice, http.MethodGet, "/api/v1/auth/me", nil), http.StatusUnauthorized, "revoked session")
	assertStatus(t, f.send(alice, http.MethodPost, "/api/v1/saved-places", map[string]any{"name": "x", "latitude": 13.7, "longitude": 100.5}), http.StatusUnauthorized, "write with revoked session")

	// Logging out again is harmless.
	assertStatus(t, f.send(alice, http.MethodPost, "/api/v1/auth/logout", nil), http.StatusNoContent, "second logout")
}

func TestLoginRotatesTheBrowsersSession(t *testing.T) {
	f := newAccessFixture()
	first := f.signUp(t, "erin@example.com")

	// Signing in while holding a session (or one planted beforehand)
	// replaces it rather than reusing it.
	w := f.send(first, http.MethodPost, "/api/v1/auth/login", map[string]any{"email": "erin@example.com", "password": "correct horse"})
	assertStatus(t, w, http.StatusOK, "login")
	second := sessionFrom(t, w)
	if second.cookie == first.cookie || second.csrf == first.csrf {
		t.Fatal("login must issue a new session and CSRF token")
	}
	assertStatus(t, f.send(first, http.MethodGet, "/api/v1/auth/me", nil), http.StatusUnauthorized, "old session after login")
	assertStatus(t, f.send(second, http.MethodGet, "/api/v1/auth/me", nil), http.StatusOK, "new session")

	// The old CSRF token doesn't work with the new session.
	stale := browserSession{cookie: second.cookie, csrf: first.csrf}
	w = f.send(stale, http.MethodPost, "/api/v1/events", eventBody())
	assertStatus(t, w, http.StatusForbidden, "old CSRF token")
}

func TestSignedInWritesNeedCSRFTokenAndTrustedOrigin(t *testing.T) {
	f := newAccessFixture()
	reportID := f.seedReport()
	alice := f.signUp(t, "alice@example.com")
	w := f.send(alice, http.MethodPost, "/api/v1/events", eventBody())
	assertStatus(t, w, http.StatusCreated, "create event")
	eventID := decodeJSON[eventResponse](t, w).ID
	w = f.send(alice, http.MethodPost, "/api/v1/saved-places", map[string]any{"name": "บ้าน", "icon": "home", "latitude": 13.72, "longitude": 100.53})
	assertStatus(t, w, http.StatusCreated, "save place")

	writes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/reports/" + reportID.String() + "/reactions", map[string]any{"type": "like"}},
		{http.MethodDelete, "/api/v1/reports/" + reportID.String() + "/reactions", nil},
		{http.MethodPost, "/api/v1/reports", map[string]any{"type": "construction", "severity": "moderate", "latitude": 13.75, "longitude": 100.5}},
		{http.MethodPost, "/api/v1/events", eventBody()},
		{http.MethodPatch, "/api/v1/events/" + eventID, map[string]any{"title": "x"}},
		{http.MethodDelete, "/api/v1/events/" + eventID, nil},
		{http.MethodPut, "/api/v1/helpers/me", map[string]any{}},
		{http.MethodPost, "/api/v1/sos", map[string]any{}},
		{http.MethodPost, "/api/v1/auth/logout", nil},
	}
	for _, c := range writes {
		what := c.method + " " + c.path
		w := sendAs(f.router, alice, c.method, c.path, c.body, func(r *http.Request) { r.Header.Set("Origin", webOrigin) })
		assertStatus(t, w, http.StatusForbidden, "no CSRF: "+what)
		if !strings.Contains(w.Body.String(), `"CSRF_TOKEN_MISSING"`) {
			t.Errorf("no CSRF %s: %s", what, w.Body)
		}
		w = sendAs(f.router, alice, c.method, c.path, c.body, func(r *http.Request) {
			r.Header.Set("Origin", webOrigin)
			r.Header.Set(csrfHeader, csrfToken("someone-elses-session"))
		})
		assertStatus(t, w, http.StatusForbidden, "wrong CSRF: "+what)
		if !strings.Contains(w.Body.String(), `"CSRF_TOKEN_INVALID"`) {
			t.Errorf("wrong CSRF %s: %s", what, w.Body)
		}
		w = sendAs(f.router, alice, c.method, c.path, c.body, func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example")
			r.Header.Set(csrfHeader, alice.csrf)
		})
		assertStatus(t, w, http.StatusForbidden, "foreign origin: "+what)
		if !strings.Contains(w.Body.String(), `"INVALID_ORIGIN"`) {
			t.Errorf("foreign origin %s: %s", what, w.Body)
		}
	}
	// Nothing above reached a handler.
	if len(f.reports.reactions) != 0 || len(f.events.items) != 1 || len(f.users.sessions) != 1 {
		t.Errorf("a rejected write had an effect: reactions=%d events=%d sessions=%d", len(f.reports.reactions), len(f.events.items), len(f.users.sessions))
	}

	// Referer is the fallback when there's no Origin; "null" is never trusted.
	w = sendAs(f.router, alice, http.MethodPost, "/api/v1/reports/"+reportID.String()+"/reactions", map[string]any{"type": "like"}, func(r *http.Request) {
		r.Header.Set("Referer", "https://evil.example/page")
		r.Header.Set(csrfHeader, alice.csrf)
	})
	assertStatus(t, w, http.StatusForbidden, "foreign referer")
	w = sendAs(f.router, alice, http.MethodPost, "/api/v1/reports/"+reportID.String()+"/reactions", map[string]any{"type": "like"}, func(r *http.Request) {
		r.Header.Set("Origin", "null")
		r.Header.Set(csrfHeader, alice.csrf)
	})
	assertStatus(t, w, http.StatusForbidden, "null origin")
	w = sendAs(f.router, alice, http.MethodPost, "/api/v1/reports/"+reportID.String()+"/reactions", map[string]any{"type": "like"}, func(r *http.Request) {
		r.Header.Set("Referer", webOrigin+"/?report="+reportID.String())
		r.Header.Set(csrfHeader, alice.csrf)
	})
	assertStatus(t, w, http.StatusOK, "trusted referer")

	// With the right token from the web app, every write goes through.
	assertStatus(t, f.send(alice, http.MethodPost, "/api/v1/reports/"+reportID.String()+"/reactions", map[string]any{"type": "like"}), http.StatusOK, "react")
	assertStatus(t, f.send(alice, http.MethodDelete, "/api/v1/reports/"+reportID.String()+"/reactions", nil), http.StatusOK, "unreact")
	assertStatus(t, f.send(alice, http.MethodPatch, "/api/v1/events/"+eventID, map[string]any{"title": "ใหม่"}), http.StatusOK, "edit event")
	assertStatus(t, f.send(alice, http.MethodDelete, "/api/v1/events/"+eventID, nil), http.StatusNoContent, "delete event")

	// Reads never need a CSRF token.
	assertStatus(t, sendAs(f.router, alice, http.MethodGet, "/api/v1/saved-places", nil, nil), http.StatusOK, "read without CSRF")
	assertStatus(t, sendAs(f.router, alice, http.MethodGet, "/api/v1/events/mine", nil, nil), http.StatusOK, "my events without CSRF")
}

func TestGuestWritesNeedNoCSRF(t *testing.T) {
	f := newAccessFixture()
	// A guest's public incident report: no cookie, no token, any page.
	w := sendAs(f.router, guest, http.MethodPost, "/api/v1/reports", map[string]any{
		"type": "flooded", "severity": "high", "latitude": 13.75, "longitude": 100.5,
	}, nil)
	assertStatus(t, w, http.StatusCreated, "guest report")
	// A guest holding a dead cookie is still a guest.
	w = sendAs(f.router, browserSession{cookie: "expired"}, http.MethodPost, "/api/v1/reports", map[string]any{
		"type": "accident", "severity": "high", "latitude": 13.75, "longitude": 100.5,
	}, nil)
	assertStatus(t, w, http.StatusCreated, "guest report with a stale cookie")
}

func TestSignInRejectsForeignOrigins(t *testing.T) {
	f := newAccessFixture()
	f.signUp(t, "frank@example.com")
	creds := map[string]any{"email": "frank@example.com", "password": "correct horse"}
	w := sendAs(f.router, guest, http.MethodPost, "/api/v1/auth/login", creds, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	assertStatus(t, w, http.StatusForbidden, "login from a foreign page")
	if sessionCookieOf(w) != nil {
		t.Error("a rejected login must not set a cookie")
	}
	w = sendAs(f.router, guest, http.MethodPost, "/api/v1/auth/register", map[string]any{"email": "g@example.com", "password": "correct horse", "display_name": "G"}, func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	})
	assertStatus(t, w, http.StatusForbidden, "register from a foreign page")
	assertStatus(t, f.send(guest, http.MethodPost, "/api/v1/auth/login", creds), http.StatusOK, "login from the web app")
}

func TestCORSAllowsOnlyTheWebOriginWithCredentials(t *testing.T) {
	f := newAccessFixture()
	preflight := func(origin string) *httptest.ResponseRecorder {
		return sendAs(f.router, guest, http.MethodOptions, "/api/v1/events", nil, func(r *http.Request) {
			r.Header.Set("Origin", origin)
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "content-type, x-csrf-token")
		})
	}

	w := preflight(webOrigin)
	assertStatus(t, w, http.StatusNoContent, "trusted preflight")
	h := w.Header()
	if h.Get("Access-Control-Allow-Origin") != webOrigin || h.Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "X-CSRF-Token") {
		t.Errorf("trusted preflight headers: %v", h)
	}

	for _, origin := range []string{"https://evil.example", "null", "http://localhost:3000.evil.example"} {
		w = preflight(origin)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q got Allow-Origin %q", origin, got)
		}
		if w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("origin %q got credentials", origin)
		}
	}

	// Never "*", and responses vary by Origin for caches.
	w = sendAs(f.router, guest, http.MethodGet, "/api/v1/events", nil, func(r *http.Request) { r.Header.Set("Origin", webOrigin) })
	if w.Header().Get("Access-Control-Allow-Origin") == "*" || w.Header().Get("Vary") != "Origin" {
		t.Errorf("simple response headers: %v", w.Header())
	}
}

func TestHTTPSOnlyTrustsForwardedProtoFromTrustedProxies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(Deps{RequireHTTPS: true, TrustedProxies: []string{"10.0.0.0/8"}, ConfigHandler: NewConfigHandler(nil, false, false)})
	call := func(remote, proto string, useTLS bool, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		req.RemoteAddr = remote
		if proto != "" {
			req.Header.Set("X-Forwarded-Proto", proto)
		}
		if useTLS {
			req.TLS = &tls.ConnectionState{}
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := call("10.1.2.3:5000", "https", false, "/api/v1/config/public")
	assertStatus(t, w, http.StatusOK, "https via trusted proxy")
	if got := w.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Errorf("HSTS = %q", got)
	}
	assertStatus(t, call("10.1.2.3:5000", "http", false, "/api/v1/config/public"), http.StatusForbidden, "http via trusted proxy")
	assertStatus(t, call("203.0.113.9:5000", "https", false, "/api/v1/config/public"), http.StatusForbidden, "forged X-Forwarded-Proto from a client")
	assertStatus(t, call("203.0.113.9:5000", "", true, "/api/v1/config/public"), http.StatusOK, "direct TLS")
	assertStatus(t, call("10.1.2.3:5000", "", false, "/healthz"), http.StatusOK, "internal health check")
}

func TestClientIPIgnoresForwardedForFromUntrustedPeers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var seen string
	r := NewRouter(Deps{TrustedProxies: []string{"10.0.0.0/8"}, ConfigHandler: NewConfigHandler(nil, false, false)})
	r.GET("/ip", func(c *gin.Context) { seen = c.ClientIP() })
	for _, tc := range []struct{ remote, want string }{
		{"203.0.113.9:1234", "203.0.113.9"}, // a client can't pick its rate-limit key
		{"10.1.2.3:1234", "198.51.100.7"},   // the platform proxy's view is believed
	} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ip", nil)
		req.RemoteAddr = tc.remote
		req.Header.Set("X-Forwarded-For", "198.51.100.7")
		r.ServeHTTP(httptest.NewRecorder(), req)
		if seen != tc.want {
			t.Errorf("peer %s: ClientIP = %s, want %s", tc.remote, seen, tc.want)
		}
	}
}

func TestOtherUsersCannotTouchMySavedPlace(t *testing.T) {
	f := newAccessFixture()
	alice := f.signUp(t, "alice@example.com")
	bob := f.signUp(t, "bob@example.com")
	w := f.send(alice, http.MethodPost, "/api/v1/saved-places", map[string]any{"name": "บ้าน", "icon": "home", "latitude": 13.72, "longitude": 100.53})
	assertStatus(t, w, http.StatusCreated, "save place")
	id := f.follows.places[0].ID

	// Someone else's place is indistinguishable from a missing one.
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		w = f.send(bob, method, "/api/v1/saved-places/"+id.String(), map[string]any{"name": "ของบ๊อบ"})
		if w.Code != http.StatusNotFound {
			t.Errorf("bob %s alice's place: %d %s", method, w.Code, w.Body)
		}
	}
	if len(f.follows.places) != 1 || *f.follows.places[0].Name != "บ้าน" {
		t.Errorf("alice's place changed: %+v", f.follows.places)
	}
	assertStatus(t, f.send(alice, http.MethodDelete, "/api/v1/saved-places/"+id.String(), nil), http.StatusNoContent, "owner deletes")
}
