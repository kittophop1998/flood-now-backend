package http

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	appevent "floodnow-api/internal/application/event"
	"floodnow-api/internal/domain/apperr"
	domainevent "floodnow-api/internal/domain/event"
	"floodnow-api/internal/ports"
)

// EventHandler serves community events: public reads (optional auth only
// adds is_mine), owner-only writes behind required auth.
type EventHandler struct {
	service      *appevent.Service
	clock        ports.Clock
	imageBaseURL string
	imageURLFn   func(baseURL, objectKey string) string
}

func NewEventHandler(service *appevent.Service, clock ports.Clock, imageBaseURL string, imageURLFn func(string, string) string) *EventHandler {
	return &EventHandler{service: service, clock: clock, imageBaseURL: imageBaseURL, imageURLFn: imageURLFn}
}

// eventResponse is public-safe: the organizer is shown by display name
// only — never their id, email or anything else from their account.
type eventResponse struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Description  *string           `json:"description"`
	Category     string            `json:"category"`
	Latitude     float64           `json:"latitude"`
	Longitude    float64           `json:"longitude"`
	LocationName *string           `json:"location_name"`
	StartAt      time.Time         `json:"start_at"`
	EndAt        time.Time         `json:"end_at"`
	ImageKey     *string           `json:"image_key"`
	ImageURL     *string           `json:"image_url"`
	Status       string            `json:"status"`
	Organizer    eventOrganizerDTO `json:"organizer"`
	IsMine       bool              `json:"is_mine"`
	CancelledAt  *time.Time        `json:"cancelled_at"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type eventOrganizerDTO struct {
	DisplayName string `json:"display_name"`
}

type eventRequest struct {
	Title        *string    `json:"title"`
	Description  *string    `json:"description"`
	Category     *string    `json:"category"`
	Latitude     *float64   `json:"latitude"`
	Longitude    *float64   `json:"longitude"`
	LocationName *string    `json:"location_name"`
	StartAt      *time.Time `json:"start_at"`
	EndAt        *time.Time `json:"end_at"`
	ImageKey     *string    `json:"image_key"`
}

func (req eventRequest) toDomain() domainevent.Fields {
	f := domainevent.Fields{
		Title: req.Title, Description: req.Description, Latitude: req.Latitude, Longitude: req.Longitude,
		LocationName: req.LocationName, StartAt: req.StartAt, EndAt: req.EndAt, ImageKey: req.ImageKey,
	}
	if req.Category != nil {
		c := domainevent.Category(*req.Category)
		f.Category = &c
	}
	return f
}

func (h *EventHandler) present(c *gin.Context, e domainevent.Event) eventResponse {
	var imageURL *string
	if e.ImageKey != nil && *e.ImageKey != "" && h.imageBaseURL != "" {
		u := h.imageURLFn(h.imageBaseURL, *e.ImageKey)
		imageURL = &u
	}
	userID := currentUserID(c)
	return eventResponse{
		ID: e.ID.String(), Title: e.Title, Description: e.Description, Category: string(e.Category),
		Latitude: e.Latitude, Longitude: e.Longitude, LocationName: e.LocationName,
		StartAt: e.StartAt.UTC(), EndAt: e.EndAt.UTC(), ImageKey: e.ImageKey, ImageURL: imageURL,
		Status:      string(e.Status(h.clock.Now())),
		Organizer:   eventOrganizerDTO{DisplayName: e.OrganizerName},
		IsMine:      userID != nil && e.IsOwnedBy(*userID),
		CancelledAt: utcPtr(e.CancelledAt), CreatedAt: e.CreatedAt.UTC(), UpdatedAt: e.UpdatedAt.UTC(),
	}
}

func (h *EventHandler) presentAll(c *gin.Context, es []domainevent.Event) []eventResponse {
	out := make([]eventResponse, 0, len(es))
	for _, e := range es {
		out = append(out, h.present(c, e))
	}
	return out
}

func (h *EventHandler) List(c *gin.Context) {
	p := newQueryParser(c)
	bbox := p.bbox()
	limit := p.int("limit")
	if err := p.err("event query is invalid"); err != nil {
		writeError(c, err)
		return
	}
	events, err := h.service.List(c.Request.Context(), bbox, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": h.presentAll(c, events)})
}

func (h *EventHandler) Mine(c *gin.Context) {
	events, err := h.service.Mine(c.Request.Context(), mustUserID(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": h.presentAll(c, events)})
}

func (h *EventHandler) Get(c *gin.Context) {
	id, ok := idParam(c, "event")
	if !ok {
		return
	}
	e, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.present(c, *e))
}

func (h *EventHandler) Create(c *gin.Context) {
	var req eventRequest
	if !bindEventJSON(c, &req) {
		return
	}
	e, err := h.service.Create(c.Request.Context(), mustUserID(c), req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, h.present(c, *e))
}

func (h *EventHandler) Update(c *gin.Context) {
	id, ok := idParam(c, "event")
	if !ok {
		return
	}
	var req eventRequest
	if !bindEventJSON(c, &req) {
		return
	}
	e, err := h.service.Update(c.Request.Context(), mustUserID(c), id, req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.present(c, *e))
}

func (h *EventHandler) Cancel(c *gin.Context) {
	id, ok := idParam(c, "event")
	if !ok {
		return
	}
	e, err := h.service.Cancel(c.Request.Context(), mustUserID(c), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.present(c, *e))
}

func (h *EventHandler) Delete(c *gin.Context) {
	id, ok := idParam(c, "event")
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), mustUserID(c), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// bindEventJSON is bindJSON with a clearer message for bad timestamps.
func bindEventJSON(c *gin.Context, dst *eventRequest) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		writeError(c, apperr.Validation("request body is invalid JSON (start_at/end_at must be RFC3339)", nil))
		return false
	}
	return true
}
