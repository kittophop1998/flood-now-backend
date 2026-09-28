package http

import (
	"net/http"
	"strings"
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

// authMiddleware resolves "Authorization: Bearer <session token>" to a user.
// With required, a missing/invalid/expired session is 401 UNAUTHORIZED; the
// optional form lets guests through (an invalid token then counts as a
// guest), so public endpoints never block anyone who isn't signed in.
// Authorization itself (who may do what) stays in the application layer.
func authMiddleware(svc *appauth.Service, required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
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

// AuthHandler serves sign-up, sign-in, sign-out and the current account.
type AuthHandler struct {
	service *appauth.Service
}

func NewAuthHandler(service *appauth.Service) *AuthHandler {
	return &AuthHandler{service: service}
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

type sessionResponse struct {
	Token     string       `json:"token"`
	ExpiresIn int64        `json:"expires_in"`
	User      userResponse `json:"user"`
}

func toUserResponse(u user.User) userResponse {
	return userResponse{ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName, CreatedAt: u.CreatedAt.UTC()}
}

func toSessionResponse(s *appauth.Session) sessionResponse {
	return sessionResponse{Token: s.Token, ExpiresIn: int64(user.SessionTTL.Seconds()), User: toUserResponse(s.User)}
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if !bindJSON(c, &req) {
		return
	}
	s, err := h.service.Register(c.Request.Context(), user.RegisterInput{Email: req.Email, Password: req.Password, DisplayName: req.DisplayName}, req.DeviceID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toSessionResponse(s))
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if !bindJSON(c, &req) {
		return
	}
	s, err := h.service.Login(c.Request.Context(), req.Email, req.Password, req.DeviceID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSessionResponse(s))
}

func (h *AuthHandler) Logout(c *gin.Context) {
	token := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if err := h.service.Logout(c.Request.Context(), token); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) Me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"user": toUserResponse(*currentUser(c))})
}
