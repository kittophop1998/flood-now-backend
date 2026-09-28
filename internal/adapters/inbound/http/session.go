package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"floodnow-api/internal/domain/apperr"
)

// Browser security for cookie sessions: the session cookie itself, the
// CSRF token bound to it, the Origin allowlist and HTTPS-only transport.
// See docs/api-spec.md#accounts-auth.

// SessionCookie is how the session token travels: only ever in this
// HttpOnly cookie, host-only (no Domain) with Path=/, never in a response
// body or a header the page can read.
type SessionCookie struct {
	Name     string
	Secure   bool
	SameSite http.SameSite
}

func (sc SessionCookie) token(c *gin.Context) string {
	v, err := c.Cookie(sc.Name)
	if err != nil {
		return ""
	}
	return v
}

// set stores a just-issued token for its lifetime ttl, ending at expiresAt
// (the server-side session expiry).
func (sc SessionCookie) set(c *gin.Context, token string, expiresAt time.Time, ttl time.Duration) {
	http.SetCookie(c.Writer, sc.cookie(token, expiresAt, int(ttl.Seconds())))
}

// clear expires the cookie with the same attributes it was set with.
func (sc SessionCookie) clear(c *gin.Context) {
	http.SetCookie(c.Writer, sc.cookie("", time.Unix(0, 0), -1))
}

func (sc SessionCookie) cookie(value string, expires time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sc.Name,
		Value:    value,
		Path:     "/",
		Expires:  expires.UTC(),
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   sc.Secure,
		SameSite: sc.SameSite,
	}
}

// csrfHeader carries the CSRF token on signed-in state-changing requests.
const csrfHeader = "X-CSRF-Token"

// csrfToken is the session's synchronizer token: HMAC-SHA256 of a fixed
// label keyed by the raw session token. Only the holder of the session can
// compute it, it changes whenever the session does, it needs no storage,
// and it can't be turned back into the session token.
func csrfToken(sessionToken string) string {
	mac := hmac.New(sha256.New, []byte(sessionToken))
	mac.Write([]byte("floodnow-csrf-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func checkCSRF(sessionToken, given string) error {
	if given == "" {
		return apperr.Rejected(apperr.CodeCSRFTokenMissing, "missing CSRF token")
	}
	if !hmac.Equal([]byte(given), []byte(csrfToken(sessionToken))) {
		return apperr.Rejected(apperr.CodeCSRFTokenInvalid, "invalid CSRF token")
	}
	return nil
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// originSet is the exact web-app origins (from WEB_ORIGIN), already
// normalized by config.
type originSet map[string]bool

func newOriginSet(origins []string) originSet {
	s := originSet{}
	for _, o := range origins {
		s[o] = true
	}
	return s
}

// checkOrigin rejects a browser request from anywhere but the web app:
// Origin must be allowlisted; without Origin, Referer's origin must be. A
// request with neither is a non-browser client (curl, tests) — nothing a
// forged page can produce — so it passes; the CSRF token still applies.
func (s originSet) checkOrigin(r *http.Request) error {
	if origin := r.Header.Get("Origin"); origin != "" {
		if s[strings.ToLower(origin)] {
			return nil
		}
		return apperr.Rejected(apperr.CodeInvalidOrigin, "request origin is not allowed")
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host != "" && s[strings.ToLower(u.Scheme+"://"+u.Host)] {
			return nil
		}
		return apperr.Rejected(apperr.CodeInvalidOrigin, "request origin is not allowed")
	}
	return nil
}

// originGuard applies checkOrigin to an unauthenticated route (sign-in,
// sign-up): there's no session to bind a CSRF token to yet, but a foreign
// page must not be able to sign the browser into an account (login CSRF).
func originGuard(origins originSet) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := origins.checkOrigin(c.Request); err != nil {
			writeError(c, err)
			c.Abort()
			return
		}
		c.Next()
	}
}

// corsMiddleware answers only the allowlisted web origins, echoing the exact
// origin (never "*") with credentials so the session cookie can be sent.
// Any other origin gets no CORS headers, so the browser blocks it.
func corsMiddleware(origins originSet) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Add("Vary", "Origin")
		if origin := c.GetHeader("Origin"); origin != "" && origins[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, "+csrfHeader)
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// httpsOnlyMiddleware (production) refuses plain-http requests and sends
// HSTS for the API host (no includeSubDomains/preload: other hosts aren't
// ours to pin). TLS usually ends at the platform's edge proxy, so its
// X-Forwarded-Proto counts — but only from a trusted proxy address, never
// from an arbitrary client. /healthz stays open for internal health checks.
func httpsOnlyMiddleware(trusted []*net.IPNet) gin.HandlerFunc {
	var once sync.Once
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/healthz" {
			c.Next()
			return
		}
		if !isHTTPS(c.Request, trusted) {
			once.Do(func() {
				log.Printf("rejecting non-https request (peer %s, X-Forwarded-Proto %q); if the edge proxy terminates TLS, list its address in TRUSTED_PROXIES",
					c.RemoteIP(), c.GetHeader("X-Forwarded-Proto"))
			})
			writeError(c, apperr.Forbidden("https is required"))
			c.Abort()
			return
		}
		c.Header("Strict-Transport-Security", "max-age=31536000")
		c.Next()
	}
}

func isHTTPS(r *http.Request, trusted []*net.IPNet) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trusted {
		if n.Contains(ip) {
			// The nearest proxy's view of the scheme (last value if chained).
			proto := r.Header.Get("X-Forwarded-Proto")
			if i := strings.LastIndex(proto, ","); i >= 0 {
				proto = proto[i+1:]
			}
			return strings.EqualFold(strings.TrimSpace(proto), "https")
		}
	}
	return false
}

// parseTrustedProxies turns config IPs/CIDRs (validated at load) into nets.
func parseTrustedProxies(list []string) []*net.IPNet {
	var out []*net.IPNet
	for _, p := range list {
		if !strings.Contains(p, "/") {
			if ip := net.ParseIP(p); ip != nil && ip.To4() != nil {
				p += "/32"
			} else {
				p += "/128"
			}
		}
		if _, n, err := net.ParseCIDR(p); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// noStore keeps session/CSRF responses out of every cache.
func noStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}
