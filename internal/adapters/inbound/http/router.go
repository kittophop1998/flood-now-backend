// Package http is the inbound Gin adapter: routing, request/response DTOs,
// and error mapping. Handlers hold no business logic — they translate
// HTTP <-> application-layer calls only.
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type Deps struct {
	ReportHandler *ReportHandler
	UploadHandler *UploadHandler
	WebOrigin     string
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

	v1 := r.Group("/api/v1")
	{
		v1.GET("/reports", deps.ReportHandler.List)
		v1.GET("/reports/:id", deps.ReportHandler.Get)
		v1.POST("/reports", deps.ReportHandler.Create)
		v1.POST("/reports/:id/confirmations", deps.ReportHandler.Confirm)
		v1.POST("/uploads/presign", deps.UploadHandler.Presign)
	}

	return r
}

func corsMiddleware(webOrigin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", webOrigin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
		c.Header("Access-Control-Max-Age", "600")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
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
