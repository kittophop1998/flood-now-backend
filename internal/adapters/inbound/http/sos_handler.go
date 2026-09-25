package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	appsos "floodnow-api/internal/application/sos"
	domainsos "floodnow-api/internal/domain/sos"
)

// SOSHandler serves SOS requests and helper mode. Every endpoint takes the
// caller's device_id; the service decides what that device may see or do.
type SOSHandler struct {
	service *appsos.Service
}

func NewSOSHandler(service *appsos.Service) *SOSHandler {
	return &SOSHandler{service: service}
}

func toSOSResponse(v appsos.View) sosResponse {
	events := make([]sosEventResponse, 0, len(v.Events))
	for _, e := range v.Events {
		events = append(events, sosEventResponse{Status: string(e.Status), Actor: string(e.Actor), CreatedAt: e.CreatedAt.UTC()})
	}
	out := sosResponse{
		ID: v.ID.String(), Role: string(v.Role), Type: string(v.Type), Description: v.Description,
		Latitude: v.Latitude, Longitude: v.Longitude, PeopleCount: v.PeopleCount, ContactPhone: v.ContactPhone,
		Status: string(v.Status), CreatedAt: v.CreatedAt.UTC(), UpdatedAt: v.UpdatedAt.UTC(), ClosedAt: utcPtr(v.ClosedAt),
		Events: events, NearbyHelperCount: v.NearbyHelpers,
	}
	if v.Helper != nil {
		out.Helper = &sosHelperResponse{
			DisplayName: v.Helper.DisplayName, ContactPhone: v.Helper.ContactPhone,
			Capabilities: capabilityStrings(v.Helper.Capabilities),
		}
	}
	return out
}

func (h *SOSHandler) Create(c *gin.Context) {
	var req createSOSRequest
	if !bindJSON(c, &req) {
		return
	}
	v, err := h.service.Create(c.Request.Context(), domainsos.NewRequestInput{
		DeviceID: req.DeviceID, ClientID: req.ClientID, Type: domainsos.Type(req.Type), Description: req.Description,
		Latitude: req.Latitude, Longitude: req.Longitude, PeopleCount: req.PeopleCount, ContactPhone: req.ContactPhone,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toSOSResponse(*v))
}

func (h *SOSHandler) Mine(c *gin.Context) {
	views, err := h.service.Mine(c.Request.Context(), c.Query("device_id"))
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]sosResponse, 0, len(views))
	for _, v := range views {
		out = append(out, toSOSResponse(v))
	}
	c.JSON(http.StatusOK, gin.H{"sos": out})
}

func (h *SOSHandler) Get(c *gin.Context) {
	id, ok := idParam(c, "sos")
	if !ok {
		return
	}
	v, err := h.service.Get(c.Request.Context(), c.Query("device_id"), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSOSResponse(*v))
}

func (h *SOSHandler) UpdateStatus(c *gin.Context) {
	id, ok := idParam(c, "sos")
	if !ok {
		return
	}
	var req sosStatusRequest
	if !bindJSON(c, &req) {
		return
	}
	v, err := h.service.UpdateStatus(c.Request.Context(), req.DeviceID, id, domainsos.Status(req.Status))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSOSResponse(*v))
}

func (h *SOSHandler) Accept(c *gin.Context) {
	id, ok := idParam(c, "sos")
	if !ok {
		return
	}
	var req sosDeviceRequest
	if !bindJSON(c, &req) {
		return
	}
	v, err := h.service.Accept(c.Request.Context(), req.DeviceID, id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSOSResponse(*v))
}

func (h *SOSHandler) Nearby(c *gin.Context) {
	p := newQueryParser(c)
	lat := p.float("lat", true)
	lng := p.float("lng", true)
	if err := p.err("nearby query is invalid"); err != nil {
		writeError(c, err)
		return
	}
	items, err := h.service.Nearby(c.Request.Context(), c.Query("device_id"), lat, lng)
	if err != nil {
		writeError(c, err)
		return
	}
	// Redacted: rounded location, no contact, no requester identity.
	out := make([]nearbySOSResponse, 0, len(items))
	for _, it := range items {
		out = append(out, nearbySOSResponse{
			ID: it.ID.String(), Type: string(it.Type), Description: it.Description,
			ApproxLatitude: domainsos.ApproximateCoordinate(it.Latitude), ApproxLongitude: domainsos.ApproximateCoordinate(it.Longitude),
			DistanceM: it.DistanceM, PeopleCount: it.PeopleCount, CreatedAt: it.CreatedAt.UTC(),
			RequiredCapabilities: capabilityStrings(it.Type.RequiredCapabilities()),
		})
	}
	c.JSON(http.StatusOK, gin.H{"sos": out})
}

func (h *SOSHandler) GetHelper(c *gin.Context) {
	helper, err := h.service.Helper(c.Request.Context(), c.Query("device_id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"helper": toHelperResponse(helper)})
}

func (h *SOSHandler) SaveHelper(c *gin.Context) {
	var req helperRequest
	if !bindJSON(c, &req) {
		return
	}
	helper, err := h.service.SaveHelper(c.Request.Context(), req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"helper": toHelperResponse(helper)})
}
