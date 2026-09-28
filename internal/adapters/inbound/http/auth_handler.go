package http

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appauth "floodnow-api/internal/application/auth"
	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/user"
)

// ctxUserKey holds the signed-in *user.User on the Gin context.
const ctxUserKey = "floodnow.user"

// authMiddleware resolves the session cookie (never a header or anything
// else the client says about who it is) to a user. With required, a
// missing/invalid/revoked/expired session is 401 UNAUTHORIZED; the optional
// form lets guests through (an invalid cookie then counts as a guest), so
// public endpoints never block anyone who isn't signed in.
//
// A signed-in state-changing request must also come from an allowlisted
// origin and carry the session's CSRF token — checked here, before any
// handler runs. A cookie-less guest request has nothing to forge and skips
// both. Authorization itself (who may do what) stays in the application
// layer.
func authMiddleware(svc *appauth.Service, cookie SessionCookie, origins originSet, required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := cookie.token(c)
		var u *user.User
		if token != "" && svc != nil {
			var err error
			if u, err = svc.Authenticate(c.Request.Context(), token); err != nil {
				writeError(c, err)
				c.Abort()
				return
			}
		}
		if u == nil && required {
			writeError(c, apperr.Unauthorized("sign in to use this feature"))
			c.Abort()
			return
		}
		if u != nil {
			if !isSafeMethod(c.Request.Method) {
				err := origins.checkOrigin(c.Request)
				if err == nil {
					err = checkCSRF(token, c.GetHeader(csrfHeader))
				}
				if err != nil {
					writeError(c, err)
					c.Abort()
					return
				}
			}
			c.Set(ctxUserKey, u)
		}
		c.Next()
	}
}

// currentUser is the signed-in user, or nil for a guest.
func currentUser(c *gin.Context) *user.User {
	if v, ok := c.Get(ctxUserKey); ok {
		return v.(*user.User)
	}
	return nil
}

// currentUserID is the signed-in user's id, or nil for a guest.
func currentUserID(c *gin.Context) *uuid.UUID {
	if u := currentUser(c); u != nil {
		id := u.ID
		return &id
	}
	return nil
}

// mustUserID is for routes behind authMiddleware(required=true).
func mustUserID(c *gin.Context) uuid.UUID {
	return currentUser(c).ID
}

// rateLimiter is a small in-memory fixed-window counter per key (client IP).
// Per API instance only — an anti-abuse brake for anonymous writes and
// password guessing, not a quota system.
type rateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	hits      map[string]*rateWindow
	lastSweep time.Time
}

type rateWindow struct {
	start time.Time
	count int
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: map[string]*rateWindow{}}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > l.window {
		for k, w := range l.hits {
			if now.Sub(w.start) >= l.window {
				delete(l.hits, k)
			}
		}
		l.lastSweep = now
	}
	w := l.hits[key]
	if w == nil || now.Sub(w.start) >= l.window {
		l.hits[key] = &rateWindow{start: now, count: 1}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}

// rateLimit applies l per client IP. With guestsOnly, signed-in users skip
// it (so it must run after authMiddleware).
func rateLimit(l *rateLimiter, guestsOnly bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if guestsOnly && currentUser(c) != nil {
			c.Next()
			return
		}
		if !l.allow(c.ClientIP(), time.Now()) {
			writeError(c, apperr.RateLimited("too many requests; try again later"))
			c.Abort()
			return
		}
		c.Next()
	}
}

// AuthHandler serves sign-up, sign-in, sign-out and the current session.
type AuthHandler struct {
	service *appauth.Service
	cookie  SessionCookie
}

func NewAuthHandler(service *appauth.Service, cookie SessionCookie) *AuthHandler {
	return &AuthHandler{service: service, cookie: cookie}
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	// DeviceID (optional) is the anonymous device used so far; its saved
	// places from before accounts move to the account.
	DeviceID string `json:"device_id"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	DeviceID string `json:"device_id"`
}

// userResponse is the signed-in user's own account. Only ever returned to
// that user — other people see at most display_name.
type userResponse struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

// sessionResponse answers sign-up/sign-in. The session token itself is only
// in the HttpOnly cookie; the page gets the CSRF token to echo back.
type sessionResponse struct {
	ExpiresIn int64        `json:"expires_in"`
	User      userResponse `json:"user"`
	CSRFToken string       `json:"csrf_token"`
}

// sessionStateResponse is GET /auth/session: a guest is user=null.
type sessionStateResponse struct {
	User      *userResponse `json:"user"`
	CSRFToken *string       `json:"csrf_token"`
}

func toUserResponse(u user.User) userResponse {
	return userResponse{ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName, CreatedAt: u.CreatedAt.UTC()}
}

// startSession sets the new session's cookie and answers with its metadata.
func (h *AuthHandler) startSession(c *gin.Context, status int, s *appauth.Session) {
	h.cookie.set(c, s.Token, s.ExpiresAt, user.SessionTTL)
	c.JSON(status, sessionResponse{ExpiresIn: int64(user.SessionTTL.Seconds()), User: toUserResponse(s.User), CSRFToken: csrfToken(s.Token)})
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if !bindJSON(c, &req) {
		return
	}
	s, err := h.service.Register(c.Request.Context(), user.RegisterInput{Email: req.Email, Password: req.Password, DisplayName: req.DisplayName}, req.DeviceID, h.cookie.token(c))
	if err != nil {
		writeError(c, err)
		return
	}
	h.startSession(c, http.StatusCreated, s)
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if !bindJSON(c, &req) {
		return
	}
	s, err := h.service.Login(c.Request.Context(), req.Email, req.Password, req.DeviceID, h.cookie.token(c))
	if err != nil {
		writeError(c, err)
		return
	}
	h.startSession(c, http.StatusOK, s)
}

// Logout revokes the server-side session and clears the cookie. Behind
// optionalAuth: a live session needs its CSRF token like any signed-in
// write; with no live session there is nothing to revoke, so it just
// clears the cookie (idempotent).
func (h *AuthHandler) Logout(c *gin.Context) {
	if currentUser(c) != nil {
		if err := h.service.Logout(c.Request.Context(), h.cookie.token(c)); err != nil {
			writeError(c, err)
			return
		}
	}
	h.cookie.clear(c)
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) Me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"user": toUserResponse(*currentUser(c)), "csrf_token": csrfToken(h.cookie.token(c))})
}

// Session tells the page who is signed in (the cookie is unreadable to it)
// and hands it the CSRF token; a guest gets nulls, not a 401. A stale
// cookie is cleared.
func (h *AuthHandler) Session(c *gin.Context) {
	u := currentUser(c)
	if u == nil {
		if h.cookie.token(c) != "" {
			h.cookie.clear(c)
		}
		c.JSON(http.StatusOK, sessionStateResponse{})
		return
	}
	res, csrf := toUserResponse(*u), csrfToken(h.cookie.token(c))
	c.JSON(http.StatusOK, sessionStateResponse{User: &res, CSRFToken: &csrf})
}
