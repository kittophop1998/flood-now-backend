package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	approute "floodnow-api/internal/application/route"
	"floodnow-api/internal/domain/apperr"
	domainroute "floodnow-api/internal/domain/route"
)

type RouteHandler struct {
	service   *approute.Service
	presenter *ReportPresenter
}

func NewRouteHandler(service *approute.Service, presenter *ReportPresenter) *RouteHandler {
	return &RouteHandler{service: service, presenter: presenter}
}

func (h *RouteHandler) Evaluate(c *gin.Context) {
	var req evaluateRouteRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Origin == nil || req.Destination == nil {
		writeError(c, apperr.Validation("route request is invalid", map[string]string{"origin": "origin and destination are required"}))
		return
	}
	res, err := h.service.Evaluate(c.Request.Context(),
		domainroute.Point{Latitude: req.Origin.Latitude, Longitude: req.Origin.Longitude},
		domainroute.Point{Latitude: req.Destination.Latitude, Longitude: req.Destination.Longitude},
		domainroute.Vehicle(req.Vehicle),
	)
	if err != nil {
		writeError(c, err)
		return
	}

	out := evaluateRouteResponse{
		Vehicle:      string(res.Vehicle),
		EvaluatedAt:  res.EvaluatedAt.UTC(),
		DataComplete: res.DataComplete,
		Routes:       make([]routeResponse, 0, len(res.Routes)),
	}
	for _, ev := range res.Routes {
		coords := make([][2]float64, len(ev.Path))
		for i, p := range ev.Path {
			coords[i] = [2]float64{p.Longitude, p.Latitude}
		}
		incidents := make([]routeIncidentResponse, 0, len(ev.Incidents))
		for _, inc := range ev.Incidents {
			incidents = append(incidents, routeIncidentResponse{
				Report:             h.presenter.one(inc.Report),
				DistanceFromRouteM: inc.DistanceFromRouteM,
				Impact:             string(inc.Impact),
				Reason:             string(inc.Reason),
			})
		}
		out.Routes = append(out.Routes, routeResponse{
			DistanceM:     ev.DistanceM,
			DurationS:     ev.DurationS,
			Geometry:      geoJSONLineString{Type: "LineString", Coordinates: coords},
			Risk:          string(ev.Risk),
			IncidentCount: len(ev.Incidents),
			BlockingCount: ev.Blocking,
			CautionCount:  ev.Cautioning,
			Incidents:     incidents,
		})
	}
	c.JSON(http.StatusOK, out)
}
