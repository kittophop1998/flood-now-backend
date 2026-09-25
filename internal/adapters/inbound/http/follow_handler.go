package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appfollow "floodnow-api/internal/application/follow"
	"floodnow-api/internal/domain/apperr"
)

type FollowHandler struct {
	service   *appfollow.Service
	presenter *ReportPresenter
}

func NewFollowHandler(service *appfollow.Service, presenter *ReportPresenter) *FollowHandler {
	return &FollowHandler{service: service, presenter: presenter}
}

func (h *FollowHandler) List(c *gin.Context) {
	follows, err := h.service.List(c.Request.Context(), c.Query("device_id"))
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]followResponse, 0, len(follows))
	for _, f := range follows {
		out = append(out, toFollowResponse(f))
	}
	c.JSON(http.StatusOK, gin.H{"follows": out})
}

func (h *FollowHandler) Create(c *gin.Context) {
	var req createFollowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, apperr.Validation("request body is invalid JSON", nil))
		return
	}
	in, ok := req.toDomain()
	if !ok {
		writeError(c, apperr.Validation("follow is invalid", map[string]string{"report_id": "must be a UUID"}))
		return
	}

	f, err := h.service.Create(c.Request.Context(), in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toFollowResponse(*f))
}

func (h *FollowHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeError(c, apperr.Validation("invalid follow id", map[string]string{"id": "must be a UUID"}))
		return
	}
	if err := h.service.Delete(c.Request.Context(), c.Query("device_id"), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *FollowHandler) Notifications(c *gin.Context) {
	p := newQueryParser(c)
	since := p.time("since")
	if err := p.err("notification query is invalid"); err != nil {
		writeError(c, err)
		return
	}

	items, err := h.service.Notifications(c.Request.Context(), c.Query("device_id"), since)
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]notificationResponse, 0, len(items))
	for _, n := range items {
		out = append(out, notificationResponse{
			ID:        n.EventID,
			Kind:      string(n.Kind),
			CreatedAt: n.CreatedAt.UTC(),
			FollowID:  n.FollowID.String(),
			Report:    h.presenter.one(n.Report),
		})
	}
	c.JSON(http.StatusOK, gin.H{"notifications": out})
}
