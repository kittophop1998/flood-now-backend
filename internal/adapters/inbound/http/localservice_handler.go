package http

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appls "floodnow-api/internal/application/localservice"
	"floodnow-api/internal/domain/apperr"
	ls "floodnow-api/internal/domain/localservice"
	"floodnow-api/internal/ports"
)

// LocalServiceHandler serves local services (providers, service requests,
// offers, jobs, the provider wallet) and the Stripe webhook. It only
// translates HTTP; who may see or do what is decided in the service. The
// response shapes encode the privacy rule: public/pre-match views carry an
// approximate point and no contact, party views carry the rest.
type LocalServiceHandler struct {
	service      *appls.Service
	imageBaseURL string
	imageURLFn   func(baseURL, objectKey string) string
}

func NewLocalServiceHandler(service *appls.Service, imageBaseURL string, imageURLFn func(string, string) string) *LocalServiceHandler {
	return &LocalServiceHandler{service: service, imageBaseURL: imageBaseURL, imageURLFn: imageURLFn}
}

func (h *LocalServiceHandler) imageURL(key *string) *string {
	if key == nil || *key == "" || h.imageBaseURL == "" {
		return nil
	}
	u := h.imageURLFn(h.imageBaseURL, *key)
	return &u
}

// ---- DTOs ----

// providerPublicResponse is what anyone may see of a provider: no phone,
// no LINE, only an approximate base point.
type providerPublicResponse struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name"`
	Categories       []string `json:"categories"`
	Description      *string  `json:"description"`
	LocationName     *string  `json:"location_name"`
	ApproxLatitude   float64  `json:"approx_latitude"`
	ApproxLongitude  float64  `json:"approx_longitude"`
	ServiceRadiusM   int      `json:"service_radius_m"`
	BusinessHours    *string  `json:"business_hours"`
	MobileService    bool     `json:"mobile_service"`
	Available        bool     `json:"available"`
	StartingPriceTHB *int     `json:"starting_price_thb"`
	LogoURL          *string  `json:"logo_url"`
	Verified         bool     `json:"verified"`
	DistanceM        *float64 `json:"distance_m"`
}

func (h *LocalServiceHandler) providerPublic(p ls.Provider, distance float64) providerPublicResponse {
	out := providerPublicResponse{
		ID: p.ID.String(), DisplayName: p.DisplayName, Categories: categoryStrs(p.Categories), Description: p.Description,
		LocationName: p.LocationName, ApproxLatitude: ls.ApproximateCoordinate(p.Latitude), ApproxLongitude: ls.ApproximateCoordinate(p.Longitude),
		ServiceRadiusM: p.ServiceRadiusM, BusinessHours: p.BusinessHours, MobileService: p.MobileService, Available: p.Available,
		StartingPriceTHB: p.StartingPriceTHB, LogoURL: h.imageURL(p.LogoKey), Verified: p.Verified(),
	}
	if distance >= 0 {
		out.DistanceM = &distance
	}
	return out
}

func categoryStrs(cs []ls.Category) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

// providerMeResponse is the owner's own full profile.
type providerMeResponse struct {
	ID               string              `json:"id"`
	DisplayName      string              `json:"display_name"`
	Categories       []string            `json:"categories"`
	Description      *string             `json:"description"`
	Phone            string              `json:"phone"`
	LineID           *string             `json:"line_id"`
	Latitude         float64             `json:"latitude"`
	Longitude        float64             `json:"longitude"`
	LocationName     *string             `json:"location_name"`
	ServiceRadiusM   int                 `json:"service_radius_m"`
	BusinessHours    *string             `json:"business_hours"`
	MobileService    bool                `json:"mobile_service"`
	Available        bool                `json:"available"`
	StartingPriceTHB *int                `json:"starting_price_thb"`
	LogoKey          *string             `json:"logo_key"`
	LogoURL          *string             `json:"logo_url"`
	Verified         bool                `json:"verified"`
	Status           string              `json:"status"`
	CreditBalance    int                 `json:"credit_balance"`
	LowCredit        bool                `json:"low_credit"`
	Summary          *providerSummaryDTO `json:"summary,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

type providerSummaryDTO struct {
	NearbyOpen    int `json:"nearby_open"`
	OffersPending int `json:"offers_pending"`
	ActiveJobs    int `json:"active_jobs"`
	CompletedJobs int `json:"completed_jobs"`
}

func (h *LocalServiceHandler) providerMe(p *ls.Provider, sum *appls.Summary) providerMeResponse {
	out := providerMeResponse{
		ID: p.ID.String(), DisplayName: p.DisplayName, Categories: categoryStrs(p.Categories), Description: p.Description,
		Phone: p.Phone, LineID: p.LineID, Latitude: p.Latitude, Longitude: p.Longitude, LocationName: p.LocationName,
		ServiceRadiusM: p.ServiceRadiusM, BusinessHours: p.BusinessHours, MobileService: p.MobileService, Available: p.Available,
		StartingPriceTHB: p.StartingPriceTHB, LogoKey: p.LogoKey, LogoURL: h.imageURL(p.LogoKey), Verified: p.Verified(),
		Status: string(p.Status), CreditBalance: p.CreditBalance, LowCredit: h.service.Policy().LowCredit(p.CreditBalance),
		CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(),
	}
	if sum != nil {
		out.Summary = &providerSummaryDTO{NearbyOpen: sum.NearbyOpen, OffersPending: sum.OffersPending, ActiveJobs: sum.ActiveJobs, CompletedJobs: sum.CompletedJobs}
	}
	return out
}

type providerRequest struct {
	DisplayName      string   `json:"display_name"`
	Categories       []string `json:"categories"`
	Description      *string  `json:"description"`
	Phone            string   `json:"phone"`
	LineID           *string  `json:"line_id"`
	Latitude         float64  `json:"latitude"`
	Longitude        float64  `json:"longitude"`
	LocationName     *string  `json:"location_name"`
	ServiceRadiusM   int      `json:"service_radius_m"`
	BusinessHours    *string  `json:"business_hours"`
	MobileService    bool     `json:"mobile_service"`
	Available        bool     `json:"available"`
	StartingPriceTHB *int     `json:"starting_price_thb"`
	LogoKey          *string  `json:"logo_key"`
}

func (req providerRequest) toDomain() ls.ProviderInput {
	cats := make([]ls.Category, len(req.Categories))
	for i, c := range req.Categories {
		cats[i] = ls.Category(c)
	}
	return ls.ProviderInput{
		DisplayName: req.DisplayName, Categories: cats, Description: req.Description, Phone: req.Phone, LineID: req.LineID,
		Latitude: req.Latitude, Longitude: req.Longitude, LocationName: req.LocationName, ServiceRadiusM: req.ServiceRadiusM,
		BusinessHours: req.BusinessHours, MobileService: req.MobileService, Available: req.Available,
		StartingPriceTHB: req.StartingPriceTHB, LogoKey: req.LogoKey,
	}
}

type offerBriefDTO struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	PriceTHB   *int      `json:"price_thb"`
	ETAMinutes int       `json:"eta_minutes"`
	Note       *string   `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func offerBrief(o ls.Offer, status ls.OfferStatus) offerBriefDTO {
	return offerBriefDTO{ID: o.ID.String(), Status: string(status), PriceTHB: o.PriceTHB, ETAMinutes: o.ETAMinutes, Note: o.Note,
		CreatedAt: o.CreatedAt.UTC(), UpdatedAt: o.UpdatedAt.UTC()}
}

// customerOfferDTO is an offer as the customer compares it.
type customerOfferDTO struct {
	offerBriefDTO
	DistanceM float64                `json:"distance_m"`
	Provider  providerPublicResponse `json:"provider"`
}

// requestRedactedDTO is a request as a provider sees it before a match:
// approximate area and distance, no contact, no exact point.
type requestRedactedDTO struct {
	ID                 string         `json:"id"`
	Category           string         `json:"category"`
	Description        *string        `json:"description"`
	VehicleInfo        *string        `json:"vehicle_info"`
	ImageURL           *string        `json:"image_url"`
	ApproxLatitude     float64        `json:"approx_latitude"`
	ApproxLongitude    float64        `json:"approx_longitude"`
	DistanceM          float64        `json:"distance_m"`
	Status             string         `json:"status"`
	SelectionExpiresAt *time.Time     `json:"selection_expires_at"`
	ExpiresAt          time.Time      `json:"expires_at"`
	CreatedAt          time.Time      `json:"created_at"`
	MyOffer            *offerBriefDTO `json:"my_offer"`
}

func (h *LocalServiceHandler) requestRedacted(r ls.Request, distance float64, now time.Time) requestRedactedDTO {
	return requestRedactedDTO{
		ID: r.ID.String(), Category: string(r.Category), Description: r.Description, VehicleInfo: r.VehicleInfo,
		ImageURL: h.imageURL(r.ImageKey), ApproxLatitude: ls.ApproximateCoordinate(r.Latitude), ApproxLongitude: ls.ApproximateCoordinate(r.Longitude),
		DistanceM: distance, Status: string(r.Effective(now)), SelectionExpiresAt: utcPtr(r.SelectionExpiresAt),
		ExpiresAt: r.ExpiresAt.UTC(), CreatedAt: r.CreatedAt.UTC(),
	}
}

type requestEventDTO struct {
	Status    string    `json:"status"`
	Actor     string    `json:"actor"`
	Note      *string   `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type matchDTO struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedTravelAt *time.Time `json:"started_travel_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	CancelledBy     *string    `json:"cancelled_by"`
	CancelReason    *string    `json:"cancel_reason"`
	// Provider's view only: what the match cost them.
	FeeCredits *int  `json:"fee_credits,omitempty"`
	FeeWaived  *bool `json:"fee_waived,omitempty"`
}

func toMatchDTO(m ls.Match, withFee bool) *matchDTO {
	out := &matchDTO{ID: m.ID.String(), Status: string(m.Status), CreatedAt: m.CreatedAt.UTC(), StartedTravelAt: utcPtr(m.StartedTravelAt),
		ClosedAt: utcPtr(m.ClosedAt), CancelReason: m.CancelReason}
	if m.CancelledBy != nil {
		s := string(*m.CancelledBy)
		out.CancelledBy = &s
	}
	if withFee {
		fee, waived := m.FeeCredits, m.FeeWaived
		out.FeeCredits, out.FeeWaived = &fee, &waived
	}
	return out
}

type providerContactDTO struct {
	ID           string  `json:"id"`
	DisplayName  string  `json:"display_name"`
	Phone        string  `json:"phone"`
	LineID       *string `json:"line_id"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	LocationName *string `json:"location_name"`
	Verified     bool    `json:"verified"`
	LogoURL      *string `json:"logo_url"`
}

type customerContactDTO struct {
	DisplayName  string `json:"display_name"`
	ContactPhone string `json:"contact_phone"`
}

// serviceRequestResponse is a request as one of its parties sees it: the
// customer always; the matched provider while the match is active/done.
type serviceRequestResponse struct {
	ID                 string              `json:"id"`
	Role               string              `json:"role"`
	Category           string              `json:"category"`
	Description        *string             `json:"description"`
	VehicleInfo        *string             `json:"vehicle_info"`
	ImageKey           *string             `json:"image_key"`
	ImageURL           *string             `json:"image_url"`
	Latitude           float64             `json:"latitude"`
	Longitude          float64             `json:"longitude"`
	LocationName       *string             `json:"location_name"`
	ContactPhone       string              `json:"contact_phone"`
	Status             string              `json:"status"`
	SelectedOfferID    *string             `json:"selected_offer_id"`
	SelectionExpiresAt *time.Time          `json:"selection_expires_at"`
	ExpiresAt          time.Time           `json:"expires_at"`
	CancelReason       *string             `json:"cancel_reason"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
	ClosedAt           *time.Time          `json:"closed_at"`
	Events             []requestEventDTO   `json:"events"`
	Offers             []customerOfferDTO  `json:"offers"`
	Match              *matchDTO           `json:"match"`
	AgreedOffer        *offerBriefDTO      `json:"agreed_offer"`
	Provider           *providerContactDTO `json:"provider"`
	Customer           *customerContactDTO `json:"customer"`
	RefundUntil        *time.Time          `json:"refund_until"`
}

func (h *LocalServiceHandler) requestView(v appls.RequestView) serviceRequestResponse {
	out := serviceRequestResponse{
		ID: v.ID.String(), Role: string(v.Role), Category: string(v.Category), Description: v.Description, VehicleInfo: v.VehicleInfo,
		ImageKey: v.ImageKey, ImageURL: h.imageURL(v.ImageKey), Latitude: v.Latitude, Longitude: v.Longitude,
		LocationName: v.LocationName, ContactPhone: v.ContactPhone, Status: string(v.Effective),
		ExpiresAt: v.ExpiresAt.UTC(), CancelReason: v.CancelReason, CreatedAt: v.CreatedAt.UTC(), UpdatedAt: v.UpdatedAt.UTC(),
		ClosedAt: utcPtr(v.ClosedAt), Events: []requestEventDTO{}, Offers: []customerOfferDTO{}, RefundUntil: utcPtr(v.RefundUntil),
	}
	if v.Effective == ls.StatusPendingConfirmation && v.SelectedOfferID != nil {
		id := v.SelectedOfferID.String()
		out.SelectedOfferID, out.SelectionExpiresAt = &id, utcPtr(v.SelectionExpiresAt)
	}
	for _, e := range v.Events {
		out.Events = append(out.Events, requestEventDTO{Status: string(e.Status), Actor: string(e.Actor), Note: e.Note, CreatedAt: e.CreatedAt.UTC()})
	}
	for _, o := range v.Offers {
		out.Offers = append(out.Offers, customerOfferDTO{
			offerBriefDTO: offerBrief(o.Offer, o.Effective), DistanceM: o.DistanceM, Provider: h.providerPublic(o.Provider, o.DistanceM),
		})
	}
	if v.Match != nil {
		out.Match = toMatchDTO(*v.Match, v.Role == ls.RoleProvider)
	}
	if v.MatchedOffer != nil {
		b := offerBrief(*v.MatchedOffer, v.MatchedOffer.Status)
		out.AgreedOffer = &b
	}
	if v.Provider != nil {
		p := v.Provider
		out.Provider = &providerContactDTO{ID: p.ID.String(), DisplayName: p.DisplayName, Phone: p.Phone, LineID: p.LineID,
			Latitude: p.Latitude, Longitude: p.Longitude, LocationName: p.LocationName, Verified: p.Verified(), LogoURL: h.imageURL(p.LogoKey)}
	}
	if v.Role == ls.RoleProvider && v.Match != nil {
		out.Customer = &customerContactDTO{DisplayName: v.CustomerName, ContactPhone: v.ContactPhone}
	}
	return out
}

type createServiceRequestBody struct {
	ClientID     *string  `json:"client_id"`
	Category     string   `json:"category"`
	Description  *string  `json:"description"`
	VehicleInfo  *string  `json:"vehicle_info"`
	ImageKey     *string  `json:"image_key"`
	Latitude     *float64 `json:"latitude"`
	Longitude    *float64 `json:"longitude"`
	LocationName *string  `json:"location_name"`
	ContactPhone string   `json:"contact_phone"`
}

type offerBody struct {
	PriceTHB   *int    `json:"price_thb"`
	ETAMinutes int     `json:"eta_minutes"`
	Note       *string `json:"note"`
}

type cancelBody struct {
	Reason string  `json:"reason"`
	Note   *string `json:"note"`
}

type issueBody struct {
	Reason  string  `json:"reason"`
	Details *string `json:"details"`
}

type statusBody struct {
	Status string `json:"status"`
}

type transactionDTO struct {
	ID           int64     `json:"id"`
	Type         string    `json:"type"`
	Amount       int       `json:"amount"`
	BalanceAfter int       `json:"balance_after"`
	MatchID      *string   `json:"match_id"`
	TopupID      *string   `json:"topup_id"`
	Note         *string   `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
}

func toTransactionDTO(t ls.Transaction) transactionDTO {
	return transactionDTO{ID: t.ID, Type: string(t.Type), Amount: t.Amount, BalanceAfter: t.BalanceAfter,
		MatchID: uuidStrPtr(t.MatchID), TopupID: uuidStrPtr(t.TopupID), Note: t.Note, CreatedAt: t.CreatedAt.UTC()}
}

func uuidStrPtr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

type topupDTO struct {
	ID           string     `json:"id"`
	PackageID    string     `json:"package_id"`
	AmountTHB    float64    `json:"amount_thb"`
	CreditAmount int        `json:"credit_amount"`
	Status       string     `json:"status"`
	PaidAt       *time.Time `json:"paid_at"`
	CreatedAt    time.Time  `json:"created_at"`
	// The QR to scan — only while the top-up is still pending.
	PromptPay *promptPayDTO `json:"promptpay"`
}

type promptPayDTO struct {
	QRData     string  `json:"qr_data"`
	QRImageURL *string `json:"qr_image_url"`
}

func toTopupDTO(t ls.Topup) topupDTO {
	out := topupDTO{ID: t.ID.String(), PackageID: t.PackageID, AmountTHB: float64(t.Amount) / 100, CreditAmount: t.CreditAmount,
		Status: string(t.Status), PaidAt: utcPtr(t.PaidAt), CreatedAt: t.CreatedAt.UTC()}
	if t.Status == ls.TopupPending && t.PromptPayQRData != nil {
		out.PromptPay = &promptPayDTO{QRData: *t.PromptPayQRData, QRImageURL: t.PromptPayQRImageURL}
	}
	return out
}

type packageDTO struct {
	ID      string `json:"id"`
	THB     int    `json:"thb"`
	Credits int    `json:"credits"`
}

// ---- public ----

func (h *LocalServiceHandler) ListProviders(c *gin.Context) {
	p := newQueryParser(c)
	lat := p.float("lat", true)
	lng := p.float("lng", true)
	radius := p.float("radius_m", false)
	limit := p.int("limit")
	var cat *ls.Category
	if raw := c.Query("category"); raw != "" {
		v := ls.Category(raw)
		cat = &v
	}
	if err := p.err("provider query is invalid"); err != nil {
		writeError(c, err)
		return
	}
	items, err := h.service.BrowseProviders(c.Request.Context(), ports.ProviderQuery{
		Latitude: lat, Longitude: lng, RadiusM: radius, Category: cat, Text: c.Query("q"),
		AvailableOnly: c.Query("available_only") == "true", Limit: limit,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]providerPublicResponse, 0, len(items))
	for _, it := range items {
		out = append(out, h.providerPublic(it.Provider, it.DistanceM))
	}
	c.JSON(http.StatusOK, gin.H{"providers": out})
}

func (h *LocalServiceHandler) GetProvider(c *gin.Context) {
	id, ok := idParam(c, "provider")
	if !ok {
		return
	}
	var at *[2]float64
	if c.Query("lat") != "" {
		p := newQueryParser(c)
		lat, lng := p.float("lat", true), p.float("lng", true)
		if err := p.err("location is invalid"); err != nil {
			writeError(c, err)
			return
		}
		at = &[2]float64{lat, lng}
	}
	item, err := h.service.PublicProvider(c.Request.Context(), id, at)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.providerPublic(item.Provider, item.DistanceM))
}

// ---- provider profile ----

func (h *LocalServiceHandler) GetMyProvider(c *gin.Context) {
	ctx := c.Request.Context()
	p, err := h.service.MyProvider(ctx, mustUserID(c))
	if err != nil {
		writeError(c, err)
		return
	}
	if p == nil {
		c.JSON(http.StatusOK, gin.H{"provider": nil})
		return
	}
	sum, err := h.service.Summary(ctx, p)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"provider": h.providerMe(p, &sum)})
}

func (h *LocalServiceHandler) SaveMyProvider(c *gin.Context) {
	var req providerRequest
	if !bindJSON(c, &req) {
		return
	}
	p, err := h.service.SaveProvider(c.Request.Context(), mustUserID(c), req.toDomain())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"provider": h.providerMe(p, nil)})
}

func (h *LocalServiceHandler) SetAvailability(c *gin.Context) {
	var req struct {
		Available bool `json:"available"`
	}
	if !bindJSON(c, &req) {
		return
	}
	p, err := h.service.SetAvailability(c.Request.Context(), mustUserID(c), req.Available)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"provider": h.providerMe(p, nil)})
}

// ---- customer ----

func (h *LocalServiceHandler) CreateRequest(c *gin.Context) {
	var req createServiceRequestBody
	if !bindJSON(c, &req) {
		return
	}
	if req.Latitude == nil || req.Longitude == nil {
		writeError(c, apperr.Validation("service request is invalid", map[string]string{"latitude": "is required"}))
		return
	}
	v, err := h.service.CreateRequest(c.Request.Context(), ls.NewRequestInput{
		CustomerUserID: mustUserID(c), ClientID: req.ClientID, Category: ls.Category(req.Category), Description: req.Description,
		VehicleInfo: req.VehicleInfo, ImageKey: req.ImageKey, Latitude: *req.Latitude, Longitude: *req.Longitude,
		LocationName: req.LocationName, ContactPhone: req.ContactPhone,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, h.requestView(*v))
}

func (h *LocalServiceHandler) MyRequests(c *gin.Context) {
	views, err := h.service.MyRequests(c.Request.Context(), mustUserID(c))
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]serviceRequestResponse, 0, len(views))
	for _, v := range views {
		out = append(out, h.requestView(v))
	}
	c.JSON(http.StatusOK, gin.H{"requests": out})
}

func (h *LocalServiceHandler) GetRequest(c *gin.Context) {
	id, ok := idParam(c, "service request")
	if !ok {
		return
	}
	v, err := h.service.GetRequest(c.Request.Context(), mustUserID(c), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.requestView(*v))
}

func (h *LocalServiceHandler) SelectOffer(c *gin.Context) {
	id, ok := idParam(c, "service request")
	if !ok {
		return
	}
	offerID, err := uuid.Parse(c.Param("offer_id"))
	if err != nil {
		writeError(c, apperr.Validation("invalid offer id", map[string]string{"offer_id": "must be a UUID"}))
		return
	}
	v, err := h.service.SelectOffer(c.Request.Context(), mustUserID(c), id, offerID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.requestView(*v))
}

func (h *LocalServiceHandler) UpdateStatus(c *gin.Context) {
	id, ok := idParam(c, "service request")
	if !ok {
		return
	}
	var req statusBody
	if !bindJSON(c, &req) {
		return
	}
	v, err := h.service.UpdateStatus(c.Request.Context(), mustUserID(c), id, ls.Status(req.Status))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.requestView(*v))
}

func (h *LocalServiceHandler) Cancel(c *gin.Context) {
	id, ok := idParam(c, "service request")
	if !ok {
		return
	}
	var req cancelBody
	if !bindJSON(c, &req) {
		return
	}
	v, err := h.service.Cancel(c.Request.Context(), mustUserID(c), id, ls.CancelReason(req.Reason), req.Note)
	if err != nil {
		writeError(c, err)
		return
	}
	if v == nil { // a provider backed out: no longer a party to it
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(http.StatusOK, h.requestView(*v))
}

func (h *LocalServiceHandler) ReportIssue(c *gin.Context) {
	id, ok := idParam(c, "service request")
	if !ok {
		return
	}
	var req issueBody
	if !bindJSON(c, &req) {
		return
	}
	if err := h.service.ReportIssue(c.Request.Context(), mustUserID(c), id, ls.IssueReason(req.Reason), req.Details); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- provider ----

func (h *LocalServiceHandler) NearbyRequests(c *gin.Context) {
	items, err := h.service.NearbyRequests(c.Request.Context(), mustUserID(c))
	if err != nil {
		writeError(c, err)
		return
	}
	now := h.service.Now()
	out := make([]requestRedactedDTO, 0, len(items))
	for _, it := range items {
		d := h.requestRedacted(it.Request, it.DistanceM, now)
		if it.MyOffer != nil {
			b := offerBrief(*it.MyOffer, ls.EffectiveOffer(*it.MyOffer, it.Request, now))
			d.MyOffer = &b
		}
		out = append(out, d)
	}
	c.JSON(http.StatusOK, gin.H{"requests": out})
}

func (h *LocalServiceHandler) Dismiss(c *gin.Context) {
	id, ok := idParam(c, "service request")
	if !ok {
		return
	}
	if err := h.service.Dismiss(c.Request.Context(), mustUserID(c), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *LocalServiceHandler) SendOffer(c *gin.Context) {
	id, ok := idParam(c, "service request")
	if !ok {
		return
	}
	var req offerBody
	if !bindJSON(c, &req) {
		return
	}
	o, err := h.service.SendOffer(c.Request.Context(), mustUserID(c), id, ls.OfferInput{PriceTHB: req.PriceTHB, ETAMinutes: req.ETAMinutes, Note: req.Note})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, offerBrief(*o, o.Status))
}

type providerOfferDTO struct {
	offerBriefDTO
	Request requestRedactedDTO `json:"request"`
}

func (h *LocalServiceHandler) MyOffers(c *gin.Context) {
	items, err := h.service.MyOffers(c.Request.Context(), mustUserID(c))
	if err != nil {
		writeError(c, err)
		return
	}
	now := h.service.Now()
	out := make([]providerOfferDTO, 0, len(items))
	for _, it := range items {
		r := h.requestRedacted(it.Request, it.DistanceM, now)
		if it.Effective != ls.OfferSelected {
			r.SelectionExpiresAt = nil // the deadline is only this provider's business while it's their selection
		}
		out = append(out, providerOfferDTO{offerBriefDTO: offerBrief(it.Offer, it.Effective), Request: r})
	}
	c.JSON(http.StatusOK, gin.H{"offers": out})
}

func (h *LocalServiceHandler) AcceptOffer(c *gin.Context) {
	id, ok := idParam(c, "offer")
	if !ok {
		return
	}
	v, err := h.service.AcceptOffer(c.Request.Context(), mustUserID(c), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.requestView(*v))
}

func (h *LocalServiceHandler) RejectOffer(c *gin.Context) {
	id, ok := idParam(c, "offer")
	if !ok {
		return
	}
	if err := h.service.RejectOffer(c.Request.Context(), mustUserID(c), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// jobDTO is a provider's job. The customer's contact and exact point are
// included only while the match is active or completed.
type jobDTO struct {
	Match    *matchDTO           `json:"match"`
	Offer    offerBriefDTO       `json:"offer"`
	Request  any                 `json:"request"`
	Customer *customerContactDTO `json:"customer"`
}

type jobRequestDTO struct {
	requestRedactedDTO
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	LocationName *string `json:"location_name"`
}

func (h *LocalServiceHandler) MyJobs(c *gin.Context) {
	jobs, err := h.service.MyJobs(c.Request.Context(), mustUserID(c))
	if err != nil {
		writeError(c, err)
		return
	}
	now := h.service.Now()
	out := make([]jobDTO, 0, len(jobs))
	for _, j := range jobs {
		d := jobDTO{Match: toMatchDTO(j.Match, true), Offer: offerBrief(j.Offer, j.Offer.Status)}
		redacted := h.requestRedacted(j.Request, 0, now)
		if j.Match.Status == ls.MatchCancelled {
			d.Request = redacted
		} else {
			d.Request = jobRequestDTO{requestRedactedDTO: redacted, Latitude: j.Request.Latitude, Longitude: j.Request.Longitude, LocationName: j.Request.LocationName}
			d.Customer = &customerContactDTO{DisplayName: j.CustomerName, ContactPhone: j.Request.ContactPhone}
		}
		out = append(out, d)
	}
	c.JSON(http.StatusOK, gin.H{"jobs": out})
}

// ---- wallet ----

func (h *LocalServiceHandler) Wallet(c *gin.Context) {
	w, err := h.service.Wallet(c.Request.Context(), mustUserID(c))
	if err != nil {
		writeError(c, err)
		return
	}
	pol := h.service.Policy()
	txs := make([]transactionDTO, 0, len(w.Transactions))
	for _, t := range w.Transactions {
		txs = append(txs, toTransactionDTO(t))
	}
	topups := make([]topupDTO, 0, len(w.Topups))
	for _, t := range w.Topups {
		topups = append(topups, toTopupDTO(t))
	}
	pkgs := []packageDTO{}
	if h.service.TopupsEnabled() {
		for _, p := range h.service.Packages() {
			pkgs = append(pkgs, packageDTO{ID: p.ID, THB: p.THB, Credits: p.Credits})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"balance": w.Provider.CreditBalance, "low_credit": w.LowCredit, "match_fee": pol.MatchFee,
		"credit_enabled": pol.CreditEnabled, "topup_enabled": h.service.TopupsEnabled(),
		"packages": pkgs, "transactions": txs, "topups": topups,
	})
}

func (h *LocalServiceHandler) StartTopup(c *gin.Context) {
	var req struct {
		PackageID string `json:"package_id"`
	}
	if !bindJSON(c, &req) {
		return
	}
	u := currentUser(c)
	t, err := h.service.StartTopup(c.Request.Context(), u.ID, u.Email, req.PackageID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"topup": toTopupDTO(*t)})
}

func (h *LocalServiceHandler) GetTopup(c *gin.Context) {
	id, ok := idParam(c, "top-up")
	if !ok {
		return
	}
	t, err := h.service.Topup(c.Request.Context(), mustUserID(c), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, toTopupDTO(*t))
}

// StripeWebhook is authenticated by Stripe's signature over the raw body —
// never by a user session. 2xx tells Stripe to stop retrying; a bad
// signature is 401 and a server error 500 (Stripe retries those).
func (h *LocalServiceHandler) StripeWebhook(c *gin.Context) {
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeError(c, apperr.Validation("webhook body is unreadable", nil))
		return
	}
	if err := h.service.HandlePaymentWebhook(c.Request.Context(), payload, c.GetHeader("Stripe-Signature")); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"received": true})
}

// ---- operator ----

type adminProviderDTO struct {
	providerMeResponse
	OwnerUserID string `json:"owner_user_id"`
}

func (h *LocalServiceHandler) AdminProviders(c *gin.Context) {
	ps, err := h.service.AdminProviders(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]adminProviderDTO, 0, len(ps))
	for i := range ps {
		out = append(out, adminProviderDTO{providerMeResponse: h.providerMe(&ps[i], nil), OwnerUserID: ps[i].OwnerUserID.String()})
	}
	c.JSON(http.StatusOK, gin.H{"providers": out})
}

func (h *LocalServiceHandler) AdminVerify(c *gin.Context) {
	id, ok := idParam(c, "provider")
	if !ok {
		return
	}
	var req struct {
		Verified bool `json:"verified"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if err := h.service.AdminVerify(c.Request.Context(), id, req.Verified); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *LocalServiceHandler) AdminSetStatus(c *gin.Context) {
	id, ok := idParam(c, "provider")
	if !ok {
		return
	}
	var req statusBody
	if !bindJSON(c, &req) {
		return
	}
	if err := h.service.AdminSetStatus(c.Request.Context(), id, ls.ProviderStatus(req.Status)); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *LocalServiceHandler) AdminAdjustCredit(c *gin.Context) {
	id, ok := idParam(c, "provider")
	if !ok {
		return
	}
	var req struct {
		Amount int    `json:"amount"`
		Note   string `json:"note"`
	}
	if !bindJSON(c, &req) {
		return
	}
	t, err := h.service.AdminAdjust(c.Request.Context(), id, req.Amount, req.Note)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toTransactionDTO(*t))
}

func (h *LocalServiceHandler) AdminIssues(c *gin.Context) {
	issues, err := h.service.AdminIssues(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]gin.H, 0, len(issues))
	for _, i := range issues {
		out = append(out, gin.H{"id": i.ID.String(), "match_id": i.MatchID.String(), "request_id": i.RequestID.String(),
			"reporter": i.Reporter, "reason": i.Reason, "details": i.Details, "status": i.Status, "created_at": i.CreatedAt.UTC()})
	}
	c.JSON(http.StatusOK, gin.H{"issues": out})
}
