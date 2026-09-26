package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	appannouncement "floodnow-api/internal/application/announcement"
	appplace "floodnow-api/internal/application/importantplace"
	appflood "floodnow-api/internal/application/officialflood"
	domainannouncement "floodnow-api/internal/domain/announcement"
	"floodnow-api/internal/domain/apperr"
	domainplace "floodnow-api/internal/domain/importantplace"
	domainflood "floodnow-api/internal/domain/officialflood"
	"floodnow-api/internal/ports"
)

// ImportantPlaceHandler serves the important-places map layer (public reads,
// operator writes).
type ImportantPlaceHandler struct {
	service *appplace.Service
}

func NewImportantPlaceHandler(service *appplace.Service) *ImportantPlaceHandler {
	return &ImportantPlaceHandler{service: service}
}

func (h *ImportantPlaceHandler) List(c *gin.Context) {
	p := newQueryParser(c)
	in := appplace.ListInput{BBox: p.bbox(), Limit: p.int("limit")}
	for _, v := range p.list("categories", func(s string) bool { return domainplace.Category(s).Valid() }) {
		in.Categories = append(in.Categories, domainplace.Category(v))
	}
	for _, v := range p.list("statuses", func(s string) bool { return domainplace.Status(s).Valid() }) {
		in.Statuses = append(in.Statuses, domainplace.Status(v))
	}
	if err := p.err("important places query is invalid"); err != nil {
		writeError(c, err)
		return
	}
	res, err := h.service.List(c.Request.Context(), in)
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]importantPlaceResponse, 0, len(res.Places))
	for _, pl := range res.Places {
		out = append(out, toImportantPlaceResponse(pl))
	}
	c.JSON(http.StatusOK, gin.H{"places": out, "has_more": res.HasMore})
}

func (h *ImportantPlaceHandler) Get(c *gin.Context) {
	id, ok := idParam(c, "important place")
	if !ok {
		return
	}
	pl, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toImportantPlaceResponse(*pl))
}

func (h *ImportantPlaceHandler) Create(c *gin.Context) {
	var req importantPlaceRequest
	if !bindJSON(c, &req) {
		return
	}
	pl, err := h.service.Create(c.Request.Context(), req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toImportantPlaceResponse(*pl))
}

func (h *ImportantPlaceHandler) Update(c *gin.Context) {
	id, ok := idParam(c, "important place")
	if !ok {
		return
	}
	var req importantPlaceRequest
	if !bindJSON(c, &req) {
		return
	}
	pl, err := h.service.Update(c.Request.Context(), id, req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toImportantPlaceResponse(*pl))
}

func (h *ImportantPlaceHandler) Delete(c *gin.Context) {
	id, ok := idParam(c, "important place")
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// AnnouncementHandler serves official announcements.
type AnnouncementHandler struct {
	service *appannouncement.Service
	clock   ports.Clock
}

func NewAnnouncementHandler(service *appannouncement.Service, clock ports.Clock) *AnnouncementHandler {
	return &AnnouncementHandler{service: service, clock: clock}
}

func (h *AnnouncementHandler) List(c *gin.Context) {
	p := newQueryParser(c)
	bbox := p.bbox()
	if err := p.err("announcement query is invalid"); err != nil {
		writeError(c, err)
		return
	}
	items, err := h.service.List(c.Request.Context(), bbox, c.Query("include_expired") == "true")
	if err != nil {
		writeError(c, err)
		return
	}
	h.writeList(c, items)
}

func (h *AnnouncementHandler) Get(c *gin.Context) {
	id, ok := idParam(c, "announcement")
	if !ok {
		return
	}
	a, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAnnouncementResponse(*a, h.clock.Now()))
}

func (h *AnnouncementHandler) AdminList(c *gin.Context) {
	items, err := h.service.AdminList(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	h.writeList(c, items)
}

func (h *AnnouncementHandler) writeList(c *gin.Context, items []domainannouncement.Announcement) {
	now := h.clock.Now()
	out := make([]announcementResponse, 0, len(items))
	for _, a := range items {
		out = append(out, toAnnouncementResponse(a, now))
	}
	c.JSON(http.StatusOK, gin.H{"announcements": out})
}

func (h *AnnouncementHandler) Create(c *gin.Context) {
	var req announcementRequest
	if !bindJSON(c, &req) {
		return
	}
	a, err := h.service.Create(c.Request.Context(), req.toDomain(), req.Publish)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toAnnouncementResponse(*a, h.clock.Now()))
}

func (h *AnnouncementHandler) Update(c *gin.Context) {
	id, ok := idParam(c, "announcement")
	if !ok {
		return
	}
	var req announcementRequest
	if !bindJSON(c, &req) {
		return
	}
	a, err := h.service.Update(c.Request.Context(), id, req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAnnouncementResponse(*a, h.clock.Now()))
}

func (h *AnnouncementHandler) setPublished(c *gin.Context, publish bool) {
	id, ok := idParam(c, "announcement")
	if !ok {
		return
	}
	a, err := h.service.SetPublished(c.Request.Context(), id, publish)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAnnouncementResponse(*a, h.clock.Now()))
}

func (h *AnnouncementHandler) Publish(c *gin.Context)   { h.setPublished(c, true) }
func (h *AnnouncementHandler) Unpublish(c *gin.Context) { h.setPublished(c, false) }

func (h *AnnouncementHandler) Delete(c *gin.Context) {
	id, ok := idParam(c, "announcement")
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// OfficialFloodHandler serves the official GISTDA flood-area layer. With no
// service (GISTDA not configured) the endpoint answers 404.
type OfficialFloodHandler struct {
	service *appflood.Service
}

func NewOfficialFloodHandler(service *appflood.Service) *OfficialFloodHandler {
	return &OfficialFloodHandler{service: service}
}

func (h *OfficialFloodHandler) Get(c *gin.Context) {
	if h.service == nil {
		writeError(c, apperr.NotFound("the GISTDA flood layer is not configured"))
		return
	}
	p := newQueryParser(c)
	period := domainflood.Period(c.DefaultQuery("period", string(domainflood.DefaultPeriod)))
	if !period.Valid() {
		p.fields["period"] = "must be one of 1d, 3d, 7d, 30d"
	}
	bbox := p.bbox()
	if err := p.err("flood layer query is invalid"); err != nil {
		writeError(c, err)
		return
	}
	var view *domainflood.Bounds
	if bbox != nil {
		view = &domainflood.Bounds{MinLng: bbox.MinLng, MinLat: bbox.MinLat, MaxLng: bbox.MaxLng, MaxLat: bbox.MaxLat}
	}
	layer, err := h.service.Get(c.Request.Context(), period, view)
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	c.JSON(http.StatusOK, toFloodLayerResponse(layer))
}
