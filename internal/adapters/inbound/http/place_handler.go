package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	appplace "floodnow-api/internal/application/place"
	domainplace "floodnow-api/internal/domain/place"
)

type PlaceHandler struct {
	service *appplace.Service
}

func NewPlaceHandler(service *appplace.Service) *PlaceHandler {
	return &PlaceHandler{service: service}
}

func toPlaceResponse(pl domainplace.Place) placeResponse {
	return placeResponse{Name: pl.Name, DisplayName: pl.DisplayName, Latitude: pl.Latitude, Longitude: pl.Longitude}
}

func (h *PlaceHandler) Search(c *gin.Context) {
	p := newQueryParser(c)
	near := p.bbox()
	if err := p.err("search query is invalid"); err != nil {
		writeError(c, err)
		return
	}

	places, err := h.service.Search(c.Request.Context(), c.Query("q"), c.Query("lang"), near)
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]placeResponse, 0, len(places))
	for _, pl := range places {
		out = append(out, toPlaceResponse(pl))
	}
	c.JSON(http.StatusOK, gin.H{"places": out})
}

func (h *PlaceHandler) Reverse(c *gin.Context) {
	p := newQueryParser(c)
	lat := p.float("lat", true)
	lng := p.float("lng", true)
	if err := p.err("reverse query is invalid"); err != nil {
		writeError(c, err)
		return
	}

	pl, err := h.service.Reverse(c.Request.Context(), lat, lng, c.Query("lang"))
	if err != nil {
		writeError(c, err)
		return
	}
	var out *placeResponse
	if pl != nil {
		r := toPlaceResponse(*pl)
		out = &r
	}
	c.JSON(http.StatusOK, gin.H{"place": out})
}
