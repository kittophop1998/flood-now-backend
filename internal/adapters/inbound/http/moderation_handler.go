package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	appmoderation "floodnow-api/internal/application/moderation"
	"floodnow-api/internal/domain/donation"
	"floodnow-api/internal/domain/moderation"
)

type ModerationHandler struct {
	service   *appmoderation.Service
	presenter *ReportPresenter
}

func NewModerationHandler(service *appmoderation.Service, presenter *ReportPresenter) *ModerationHandler {
	return &ModerationHandler{service: service, presenter: presenter}
}

// ReportProblem is the public "report a problem with this incident" action.
func (h *ModerationHandler) ReportProblem(c *gin.Context) {
	id, ok := idParam(c, "report")
	if !ok {
		return
	}
	var req problemReportRequest
	if !bindJSON(c, &req) {
		return
	}
	cp, err := h.service.Report(c.Request.Context(), id, moderation.NewComplaintInput{
		DeviceID: req.DeviceID, Reason: moderation.Reason(req.Reason), Details: req.Details,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, problemReportResponse{
		ID: cp.ID.String(), ReportID: cp.ReportID.String(), Reason: string(cp.Reason), Status: string(cp.Status), CreatedAt: cp.CreatedAt.UTC(),
	})
}

func (h *ModerationHandler) Queue(c *gin.Context) {
	items, err := h.service.Queue(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]moderationItemResponse, 0, len(items))
	for _, it := range items {
		reasons := map[string]int{}
		for r, n := range it.Reasons {
			reasons[string(r)] = n
		}
		complaints := make([]complaintResponse, 0, len(it.Complaints))
		for _, cp := range it.Complaints {
			complaints = append(complaints, complaintResponse{ID: cp.ID.String(), Reason: string(cp.Reason), Details: cp.Details, CreatedAt: cp.CreatedAt.UTC()})
		}
		events := make([]reportEventResponse, 0, len(it.Events))
		for _, e := range it.Events {
			events = append(events, reportEventResponse{Kind: string(e.Kind), CreatedAt: e.CreatedAt.UTC()})
		}
		out = append(out, moderationItemResponse{
			Report: h.presenter.admin(it.Report), ComplaintCount: it.ComplaintCount, Reasons: reasons,
			LatestAt: it.LatestAt.UTC(), Complaints: complaints, Events: events,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *ModerationHandler) Apply(c *gin.Context) {
	id, ok := idParam(c, "report")
	if !ok {
		return
	}
	var req moderationActionRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.service.Apply(c.Request.Context(), id, moderation.Action(req.Action)); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ConfigHandler serves non-secret runtime configuration for the web app.
type ConfigHandler struct {
	donation    *donation.Config
	gistdaFlood bool
	dohCCTV     bool
}

func NewConfigHandler(d *donation.Config, gistdaFlood, dohCCTV bool) *ConfigHandler {
	return &ConfigHandler{donation: d, gistdaFlood: gistdaFlood, dohCCTV: dohCCTV}
}

func (h *ConfigHandler) Public(c *gin.Context) {
	out := publicConfigResponse{GISTDAFlood: h.gistdaFlood, DOHCCTV: h.dohCCTV}
	if d := h.donation; d != nil {
		out.Donation = &donationConfigResponse{PromptPayID: d.PromptPayID, IDType: string(d.IDType), RecipientName: d.RecipientName}
	}
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, out)
}
