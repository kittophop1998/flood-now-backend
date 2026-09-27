package http

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	appupload "floodnow-api/internal/application/upload"
	"floodnow-api/internal/domain/apperr"
	domainupload "floodnow-api/internal/domain/upload"
)

type UploadHandler struct {
	service *appupload.Service
}

func NewUploadHandler(service *appupload.Service) *UploadHandler {
	return &UploadHandler{service: service}
}

func (h *UploadHandler) Presign(c *gin.Context) { h.presign(c, h.service.Presign) }

// AdminPresign signs official-announcement image uploads (operator token).
func (h *UploadHandler) AdminPresign(c *gin.Context) {
	h.presign(c, h.service.PresignAnnouncementImage)
}

func (h *UploadHandler) presign(c *gin.Context, sign func(context.Context, domainupload.Request) (*appupload.Result, error)) {
	var req presignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, apperr.Validation("request body is invalid JSON", nil))
		return
	}

	result, err := sign(c.Request.Context(), domainupload.Request{
		ContentType:   req.ContentType,
		ContentLength: req.ContentLength,
	})
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, presignResponse{
		ObjectKey: result.ObjectKey,
		UploadURL: result.UploadURL,
		ExpiresIn: int64(result.ExpiresIn.Seconds()),
	})
}
