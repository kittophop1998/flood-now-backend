package http

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appreport "floodnow-api/internal/application/report"
	"floodnow-api/internal/domain/apperr"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

// ReportPresenter turns domain reports into the documented JSON shape. It's
// shared by every handler that returns reports.
type ReportPresenter struct {
	clock        ports.Clock
	imageBaseURL string
	imageURLFn   func(baseURL, objectKey string) string
}

func NewReportPresenter(clock ports.Clock, imageBaseURL string, imageURLFn func(string, string) string) *ReportPresenter {
	return &ReportPresenter{clock: clock, imageBaseURL: imageBaseURL, imageURLFn: imageURLFn}
}

func (p *ReportPresenter) one(r domainreport.ReportWithStats) reportResponse {
	var imageURL *string
	if r.ImageKey != nil && *r.ImageKey != "" {
		u := p.imageURLFn(p.imageBaseURL, *r.ImageKey)
		imageURL = &u
	}
	var waterDepth *string
	if r.WaterDepth != nil {
		s := string(*r.WaterDepth)
		waterDepth = &s
	}
	var pass *passabilityDTO
	if r.Passability != nil {
		pass = &passabilityDTO{
			Walk:       string(r.Passability.Walk),
			Motorcycle: string(r.Passability.Motorcycle),
			Sedan:      string(r.Passability.Sedan),
			SUVPickup:  string(r.Passability.SUVPickup),
		}
	}
	var resolvedAt *time.Time
	if r.ResolvedAt != nil {
		t := r.ResolvedAt.UTC()
		resolvedAt = &t
	}
	now := p.clock.Now()
	return reportResponse{
		ID:               r.ID.String(),
		Type:             string(r.Type),
		Severity:         string(r.Severity),
		Status:           string(r.Status(now)),
		Latitude:         r.Latitude,
		Longitude:        r.Longitude,
		GeometryType:     string(r.GeometryType),
		WaterDepth:       waterDepth,
		WaterLevelCM:     r.WaterLevelCM,
		Passability:      pass,
		Description:      r.Description,
		ImageKey:         r.ImageKey,
		ImageURL:         imageURL,
		PeopleCount:      r.PeopleCount,
		HasChild:         r.HasChild,
		HasElderly:       r.HasElderly,
		ContactPhone:     r.ContactPhone,
		CreatedAt:        r.CreatedAt.UTC(),
		UpdatedAt:        r.UpdatedAt.UTC(),
		LastVerifiedAt:   r.LastVerifiedAt.UTC(),
		StaleAt:          r.StaleAt.UTC(),
		ExpiresAt:        r.ExpiresAt.UTC(),
		ResolvedAt:       resolvedAt,
		IsExpired:        r.IsExpired(now),
		StillActiveCount: r.StillActiveCount,
		ClearedCount:     r.ClearedCount,
		DistanceM:        r.DistanceM,
	}
}

func (p *ReportPresenter) many(rs []domainreport.ReportWithStats) []reportResponse {
	out := make([]reportResponse, 0, len(rs))
	for _, r := range rs {
		out = append(out, p.one(r))
	}
	return out
}

type ReportHandler struct {
	service   *appreport.Service
	presenter *ReportPresenter
}

func NewReportHandler(service *appreport.Service, presenter *ReportPresenter) *ReportHandler {
	return &ReportHandler{service: service, presenter: presenter}
}

// queryParser collects validation errors while reading query params so a
// request reports every bad field at once.
type queryParser struct {
	q      url.Values
	fields map[string]string
}

func newQueryParser(c *gin.Context) *queryParser {
	return &queryParser{q: c.Request.URL.Query(), fields: map[string]string{}}
}

func (p *queryParser) err(message string) error {
	if len(p.fields) == 0 {
		return nil
	}
	return apperr.Validation(message, p.fields)
}

func (p *queryParser) float(name string, required bool) float64 {
	raw := p.q.Get(name)
	if raw == "" {
		if required {
			p.fields[name] = "is required"
		}
		return 0
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		p.fields[name] = "must be a number"
	}
	return f
}

func (p *queryParser) int(name string) int {
	raw := p.q.Get(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		p.fields[name] = "must be a non-negative integer"
	}
	return n
}

func (p *queryParser) time(name string) *time.Time {
	raw := p.q.Get(name)
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		p.fields[name] = "must be an RFC3339 timestamp"
		return nil
	}
	return &t
}

// list reads a comma-separated param, checking each value with valid.
func (p *queryParser) list(name string, valid func(string) bool) []string {
	raw := p.q.Get(name)
	if raw == "" {
		return nil
	}
	var out []string
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !valid(v) {
			p.fields[name] = "contains an unknown value: " + v
			return nil
		}
		out = append(out, v)
	}
	return out
}

func (p *queryParser) types() []domainreport.Type {
	var out []domainreport.Type
	for _, v := range p.list("types", func(s string) bool { return domainreport.Type(s).Valid() }) {
		out = append(out, domainreport.Type(v))
	}
	return out
}

func (p *queryParser) statuses() []domainreport.Status {
	var out []domainreport.Status
	for _, v := range p.list("statuses", func(s string) bool { return domainreport.Status(s).Valid() }) {
		out = append(out, domainreport.Status(v))
	}
	return out
}

func (p *queryParser) bbox() *ports.BBox {
	names := []string{"min_lat", "max_lat", "min_lng", "max_lng"}
	present := 0
	for _, n := range names {
		if p.q.Has(n) {
			present++
		}
	}
	if present == 0 {
		return nil
	}
	if present != 4 {
		p.fields["bbox"] = "min_lat, max_lat, min_lng, max_lng must all be present or all omitted"
		return nil
	}
	b := ports.BBox{MinLat: p.float("min_lat", true), MaxLat: p.float("max_lat", true), MinLng: p.float("min_lng", true), MaxLng: p.float("max_lng", true)}
	switch {
	case b.MinLat > b.MaxLat || b.MinLng > b.MaxLng:
		p.fields["bbox"] = "min must be <= max"
	case b.MinLat < -90 || b.MaxLat > 90 || b.MinLng < -180 || b.MaxLng > 180:
		p.fields["bbox"] = "must be within lat [-90, 90] and lng [-180, 180]"
	}
	return &b
}

func (h *ReportHandler) List(c *gin.Context) {
	p := newQueryParser(c)
	in := appreport.ListInput{
		BBox:         p.bbox(),
		Types:        p.types(),
		Statuses:     p.statuses(),
		UpdatedSince: p.time("updated_since"),
		Limit:        p.int("limit"),
	}
	for _, v := range p.list("severities", func(s string) bool { return domainreport.Severity(s).Valid() }) {
		in.Severities = append(in.Severities, domainreport.Severity(v))
	}
	// Kept for backward compatibility: include_expired=true widens the
	// default status set to every status.
	if p.q.Get("include_expired") == "true" && len(in.Statuses) == 0 {
		in.Statuses = []domainreport.Status{domainreport.StatusActive, domainreport.StatusPossiblyStale, domainreport.StatusExpired, domainreport.StatusResolved}
	}
	if err := p.err("list query is invalid"); err != nil {
		writeError(c, err)
		return
	}

	res, err := h.service.List(c.Request.Context(), in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, listReportsResponse{Reports: h.presenter.many(res.Reports), HasMore: res.HasMore})
}

func (h *ReportHandler) Nearby(c *gin.Context) {
	p := newQueryParser(c)
	in := appreport.NearbyInput{
		Latitude:  p.float("lat", true),
		Longitude: p.float("lng", true),
		RadiusM:   p.float("radius_m", false),
		Types:     p.types(),
		Statuses:  p.statuses(),
		Sort:      ports.NearbySort(p.q.Get("sort")),
		Limit:     p.int("limit"),
	}
	if err := p.err("nearby query is invalid"); err != nil {
		writeError(c, err)
		return
	}

	reports, err := h.service.Nearby(c.Request.Context(), in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, reportsResponse{Reports: h.presenter.many(reports)})
}

func (h *ReportHandler) Duplicates(c *gin.Context) {
	p := newQueryParser(c)
	lat := p.float("lat", true)
	lng := p.float("lng", true)
	t := domainreport.Type(p.q.Get("type"))
	if err := p.err("duplicate query is invalid"); err != nil {
		writeError(c, err)
		return
	}

	reports, err := h.service.FindDuplicates(c.Request.Context(), lat, lng, t)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, reportsResponse{Reports: h.presenter.many(reports)})
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
	c.JSON(http.StatusOK, h.presenter.one(*r))
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
	c.JSON(http.StatusCreated, h.presenter.one(*r))
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
	c.JSON(http.StatusOK, h.presenter.one(*r))
}
