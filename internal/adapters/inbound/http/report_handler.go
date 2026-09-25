package http

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appreport "floodnow-api/internal/application/report"
	"floodnow-api/internal/domain/apperr"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

type ReportHandler struct {
	service      *appreport.Service
	clock        ports.Clock
	imageBaseURL string
	imageURLFn   func(baseURL, objectKey string) string
}

func NewReportHandler(service *appreport.Service, clock ports.Clock, imageBaseURL string, imageURLFn func(string, string) string) *ReportHandler {
	return &ReportHandler{service: service, clock: clock, imageBaseURL: imageBaseURL, imageURLFn: imageURLFn}
}

func (h *ReportHandler) toResponse(r domainreport.ReportWithStats) reportResponse {
	var imageURL *string
	if r.ImageKey != nil && *r.ImageKey != "" {
		u := h.imageURLFn(h.imageBaseURL, *r.ImageKey)
		imageURL = &u
	}
	return reportResponse{
		ID:               r.ID.String(),
		Type:             string(r.Type),
		Severity:         string(r.Severity),
		Latitude:         r.Latitude,
		Longitude:        r.Longitude,
		WaterLevelCM:     r.WaterLevelCM,
		Description:      r.Description,
		ImageKey:         r.ImageKey,
		ImageURL:         imageURL,
		PeopleCount:      r.PeopleCount,
		HasChild:         r.HasChild,
		HasElderly:       r.HasElderly,
		ContactPhone:     r.ContactPhone,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
		LastVerifiedAt:   r.LastVerifiedAt,
		ExpiresAt:        r.ExpiresAt,
		IsExpired:        r.IsExpired(h.clock.Now()),
		StillActiveCount: r.StillActiveCount,
		ClearedCount:     r.ClearedCount,
	}
}

func (h *ReportHandler) List(c *gin.Context) {
	q := c.Request.URL.Query()
	bboxParams := []string{"min_lat", "max_lat", "min_lng", "max_lng"}
	present := 0
	for _, p := range bboxParams {
		if q.Has(p) {
			present++
		}
	}
	if present != 0 && present != 4 {
		writeError(c, apperr.Validation("bounding box params must all be provided together", map[string]string{
			"bbox": "min_lat, max_lat, min_lng, max_lng must all be present or all omitted",
		}))
		return
	}

	var bbox *ports.BBox
	if present == 4 {
		vals := map[string]float64{}
		for _, p := range bboxParams {
			f, err := strconv.ParseFloat(q.Get(p), 64)
			if err != nil {
				writeError(c, apperr.Validation("bounding box params must be numeric", map[string]string{p: "must be a number"}))
				return
			}
			vals[p] = f
		}
		b := ports.BBox{MinLat: vals["min_lat"], MaxLat: vals["max_lat"], MinLng: vals["min_lng"], MaxLng: vals["max_lng"]}
		if b.MinLat > b.MaxLat || b.MinLng > b.MaxLng {
			writeError(c, apperr.Validation("bounding box is invalid", map[string]string{"bbox": "min must be <= max"}))
			return
		}
		bbox = &b
	}

	includeExpired := q.Get("include_expired") == "true"

	reports, err := h.service.List(c.Request.Context(), appreport.ListInput{BBox: bbox, IncludeExpired: includeExpired})
	if err != nil {
		writeError(c, err)
		return
	}

	resp := listReportsResponse{Reports: make([]reportResponse, 0, len(reports))}
	for _, r := range reports {
		resp.Reports = append(resp.Reports, h.toResponse(r))
	}
	c.JSON(http.StatusOK, resp)
}

func (h *ReportHandler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeError(c, apperr.Validation("invalid report id", map[string]string{"id": "must be a UUID"}))
		return
	}

	r, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.toResponse(*r))
}

func (h *ReportHandler) Create(c *gin.Context) {
	var req createReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, apperr.Validation("request body is invalid JSON", nil))
		return
	}

	r, err := h.service.Create(c.Request.Context(), req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, h.toResponse(*r))
}

func (h *ReportHandler) Confirm(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeError(c, apperr.Validation("invalid report id", map[string]string{"id": "must be a UUID"}))
		return
	}

	var req confirmReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, apperr.Validation("request body is invalid JSON", nil))
		return
	}

	r, err := h.service.Confirm(c.Request.Context(), id, req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.toResponse(*r))
}
