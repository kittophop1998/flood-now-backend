package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appfollow "floodnow-api/internal/application/follow"
	"floodnow-api/internal/domain/apperr"
)

// idParam parses the :id path segment, writing a validation error if it
// isn't a UUID.
func idParam(c *gin.Context, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeError(c, apperr.Validation("invalid "+what+" id", map[string]string{"id": "must be a UUID"}))
		return uuid.Nil, false
	}
	return id, true
}

// bindJSON decodes the body, writing the standard error on failure.
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		writeError(c, apperr.Validation("request body is invalid JSON", nil))
		return false
	}
	return true
}

// SavedPlaceHandler serves saved places (and their watch areas). Places are
// private to the device that saved them.
type SavedPlaceHandler struct {
	service *appfollow.Service
}

func NewSavedPlaceHandler(service *appfollow.Service) *SavedPlaceHandler {
	return &SavedPlaceHandler{service: service}
}

func (h *SavedPlaceHandler) List(c *gin.Context) {
	places, err := h.service.Places(c.Request.Context(), c.Query("device_id"))
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]savedPlaceResponse, 0, len(places))
	for _, p := range places {
		out = append(out, toSavedPlaceResponse(p))
	}
	c.JSON(http.StatusOK, gin.H{"saved_places": out})
}

func (h *SavedPlaceHandler) Create(c *gin.Context) {
	var req savedPlaceRequest
	if !bindJSON(c, &req) {
		return
	}
	p, err := h.service.CreatePlace(c.Request.Context(), req.DeviceID, req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toSavedPlaceResponse(*p))
}

func (h *SavedPlaceHandler) Update(c *gin.Context) {
	id, ok := idParam(c, "saved place")
	if !ok {
		return
	}
	var req savedPlaceRequest
	if !bindJSON(c, &req) {
		return
	}
	p, err := h.service.UpdatePlace(c.Request.Context(), req.DeviceID, id, req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSavedPlaceResponse(*p))
}

func (h *SavedPlaceHandler) Delete(c *gin.Context) {
	id, ok := idParam(c, "saved place")
	if !ok {
		return
	}
	if err := h.service.DeletePlace(c.Request.Context(), c.Query("device_id"), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
