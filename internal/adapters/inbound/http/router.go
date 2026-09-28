// Package http is the inbound Gin adapter: routing, request/response DTOs,
// and error mapping. Handlers hold no business logic — they translate
// HTTP <-> application-layer calls only.
package http

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	appauth "floodnow-api/internal/application/auth"
	"floodnow-api/internal/domain/apperr"
)

type Deps struct {
	ReportHandler         *ReportHandler
	UploadHandler         *UploadHandler
	FollowHandler         *FollowHandler
	PlaceHandler          *PlaceHandler
	SavedPlaceHandler     *SavedPlaceHandler
	RouteHandler          *RouteHandler
	SOSHandler            *SOSHandler
	ImportantPlaceHandler *ImportantPlaceHandler
	AnnouncementHandler   *AnnouncementHandler
	ModerationHandler     *ModerationHandler
	ConfigHandler         *ConfigHandler
	OfficialFloodHandler  *OfficialFloodHandler
	CCTVHandler           *CCTVHandler
	AuthHandler           *AuthHandler
	EventHandler          *EventHandler
	// AuthService resolves user session tokens; nil means nobody is ever
	// signed in (tests that don't need accounts).
	AuthService *appauth.Service
	WebOrigin   string
	// AdminToken gates /api/v1/admin/*. Empty disables those routes.
	AdminToken string
}

func NewRouter(deps Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware(deps.WebOrigin))
	r.Use(timeoutMiddleware(10 * time.Second))
	r.Use(bodyLimitMiddleware(1 << 20)) // 1 MiB — request bodies are JSON metadata only, images go straight to R2

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Guest-first: every read is public. optionalAuth only personalizes
	// (e.g. my_reaction, is_mine) and lets a signed-in reporter own a
	// report; requireAuth guards interactions, private data and owned
	// content (401 UNAUTHORIZED for guests). Who may do what is decided in
	// the application layer.
	optionalAuth := authMiddleware(deps.AuthService, false)
	requireAuth := authMiddleware(deps.AuthService, true)
	// Per-IP brakes (in-memory, per instance): anonymous report creation and
	// password guessing. Generous, since many phones share a carrier IP.
	guestReportLimit := rateLimit(newRateLimiter(30, 10*time.Minute), true)
	loginLimit := rateLimit(newRateLimiter(30, 10*time.Minute), false)
	registerLimit := rateLimit(newRateLimiter(30, time.Hour), false)

	v1 := r.Group("/api/v1")
	{
		v1.GET("/config/public", deps.ConfigHandler.Public)

		v1.POST("/auth/register", registerLimit, deps.AuthHandler.Register)
		v1.POST("/auth/login", loginLimit, deps.AuthHandler.Login)
		v1.POST("/auth/logout", requireAuth, deps.AuthHandler.Logout)
		v1.GET("/auth/me", requireAuth, deps.AuthHandler.Me)

		v1.GET("/reports", deps.ReportHandler.List)
		v1.GET("/reports/aggregate", deps.ReportHandler.Aggregate)
		v1.GET("/reports/nearby", deps.ReportHandler.Nearby)
		v1.GET("/reports/duplicates", deps.ReportHandler.Duplicates)
		v1.GET("/reports/:id", optionalAuth, deps.ReportHandler.Get)
		v1.POST("/reports", optionalAuth, guestReportLimit, deps.ReportHandler.Create)
		v1.POST("/reports/:id/confirmations", deps.ReportHandler.Confirm)
		v1.POST("/reports/:id/reactions", requireAuth, deps.ReportHandler.React)
		v1.DELETE("/reports/:id/reactions", requireAuth, deps.ReportHandler.RemoveReaction)
		v1.POST("/reports/:id/problems", deps.ModerationHandler.ReportProblem)
		v1.POST("/uploads/presign", deps.UploadHandler.Presign)

		v1.GET("/follows", deps.FollowHandler.List)
		v1.POST("/follows", deps.FollowHandler.Create)
		v1.DELETE("/follows/:id", deps.FollowHandler.Delete)
		v1.GET("/notifications", optionalAuth, deps.FollowHandler.Notifications)

		v1.GET("/places/search", deps.PlaceHandler.Search)
		v1.GET("/places/reverse", deps.PlaceHandler.Reverse)

		v1.GET("/saved-places", requireAuth, deps.SavedPlaceHandler.List)
		v1.POST("/saved-places", requireAuth, deps.SavedPlaceHandler.Create)
		v1.PATCH("/saved-places/:id", requireAuth, deps.SavedPlaceHandler.Update)
		v1.DELETE("/saved-places/:id", requireAuth, deps.SavedPlaceHandler.Delete)

		v1.POST("/routes/evaluate", deps.RouteHandler.Evaluate)

		v1.POST("/sos", requireAuth, deps.SOSHandler.Create)
		v1.GET("/sos/mine", deps.SOSHandler.Mine)
		v1.GET("/sos/:id", deps.SOSHandler.Get)
		v1.POST("/sos/:id/accept", requireAuth, deps.SOSHandler.Accept)
		v1.POST("/sos/:id/status", deps.SOSHandler.UpdateStatus)
		v1.GET("/helpers/me", deps.SOSHandler.GetHelper)
		v1.PUT("/helpers/me", requireAuth, deps.SOSHandler.SaveHelper)
		v1.GET("/helpers/sos/nearby", deps.SOSHandler.Nearby)

		v1.GET("/events", optionalAuth, deps.EventHandler.List)
		v1.GET("/events/mine", requireAuth, deps.EventHandler.Mine)
		v1.GET("/events/:id", optionalAuth, deps.EventHandler.Get)
		v1.POST("/events", requireAuth, deps.EventHandler.Create)
		v1.PATCH("/events/:id", requireAuth, deps.EventHandler.Update)
		v1.POST("/events/:id/cancel", requireAuth, deps.EventHandler.Cancel)
		v1.DELETE("/events/:id", requireAuth, deps.EventHandler.Delete)

		v1.GET("/important-places", deps.ImportantPlaceHandler.List)
		v1.GET("/important-places/:id", deps.ImportantPlaceHandler.Get)

		v1.GET("/announcements", deps.AnnouncementHandler.List)
		v1.GET("/announcements/:id", deps.AnnouncementHandler.Get)

		v1.GET("/official/gistda/flood", deps.OfficialFloodHandler.Get)

		v1.GET("/cctv", deps.CCTVHandler.List)
		v1.GET("/cctv/nearby", deps.CCTVHandler.Nearby)
		v1.GET("/cctv/:id", deps.CCTVHandler.Get)
	}

	// Operator endpoints: moderation queue, announcements, important places.
	admin := r.Group("/api/v1/admin", adminMiddleware(deps.AdminToken))
	{
		admin.GET("/moderation/queue", deps.ModerationHandler.Queue)
		admin.POST("/reports/:id/moderation", deps.ModerationHandler.Apply)

		admin.GET("/announcements", deps.AnnouncementHandler.AdminList)
		admin.POST("/announcements", deps.AnnouncementHandler.Create)
		admin.PATCH("/announcements/:id", deps.AnnouncementHandler.Update)
		admin.POST("/announcements/:id/publish", deps.AnnouncementHandler.Publish)
		admin.POST("/announcements/:id/unpublish", deps.AnnouncementHandler.Unpublish)
		admin.DELETE("/announcements/:id", deps.AnnouncementHandler.Delete)
		admin.POST("/uploads/presign", deps.UploadHandler.AdminPresign)

		admin.POST("/important-places", deps.ImportantPlaceHandler.Create)
		admin.PATCH("/important-places/:id", deps.ImportantPlaceHandler.Update)
		admin.DELETE("/important-places/:id", deps.ImportantPlaceHandler.Delete)
	}

	return r
}

func corsMiddleware(webOrigin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", webOrigin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Max-Age", "600")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// adminMiddleware requires "Authorization: Bearer <ADMIN_TOKEN>". User
// accounts never grant operator rights; this shared operator token is the
// whole admin system. With no token configured the admin API doesn't exist
// (404).
func adminMiddleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			writeError(c, apperr.NotFound("not found"))
			c.Abort()
			return
		}
		given := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			writeError(c, apperr.Unauthorized("admin token is missing or invalid"))
			c.Abort()
			return
		}
		c.Next()
	}
}

func timeoutMiddleware(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func bodyLimitMiddleware(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
