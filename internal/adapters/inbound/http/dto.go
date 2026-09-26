package http

import (
	"time"

	"github.com/google/uuid"

	appflood "floodnow-api/internal/application/officialflood"

	domainannouncement "floodnow-api/internal/domain/announcement"
	domainfollow "floodnow-api/internal/domain/follow"
	domainplace "floodnow-api/internal/domain/importantplace"
	domainflood "floodnow-api/internal/domain/officialflood"
	domainreport "floodnow-api/internal/domain/report"
	domainsos "floodnow-api/internal/domain/sos"
)

type passabilityDTO struct {
	Walk       string `json:"walk"`
	Motorcycle string `json:"motorcycle"`
	Sedan      string `json:"sedan"`
	SUVPickup  string `json:"suv_pickup"`
}

type reportResponse struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Severity         string          `json:"severity"`
	Status           string          `json:"status"`
	Latitude         float64         `json:"latitude"`
	Longitude        float64         `json:"longitude"`
	GeometryType     string          `json:"geometry_type"`
	WaterDepth       *string         `json:"water_depth"`
	WaterLevelCM     *int            `json:"water_level_cm"`
	Passability      *passabilityDTO `json:"passability"`
	Description      *string         `json:"description"`
	ImageKey         *string         `json:"image_key"`
	ImageURL         *string         `json:"image_url"`
	PeopleCount      *int            `json:"people_count"`
	HasChild         *bool           `json:"has_child"`
	HasElderly       *bool           `json:"has_elderly"`
	ContactPhone     *string         `json:"contact_phone"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	LastVerifiedAt   time.Time       `json:"last_verified_at"`
	StaleAt          time.Time       `json:"stale_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	ResolvedAt       *time.Time      `json:"resolved_at"`
	IsExpired        bool            `json:"is_expired"`
	StillActiveCount int             `json:"still_active_count"`
	ClearedCount     int             `json:"cleared_count"`
	DistanceM        *float64        `json:"distance_m,omitempty"`
}

type listReportsResponse struct {
	Reports []reportResponse `json:"reports"`
	HasMore bool             `json:"has_more"`
}

type reportsResponse struct {
	Reports []reportResponse `json:"reports"`
}

type createReportRequest struct {
	Type         string          `json:"type"`
	Severity     string          `json:"severity"`
	Latitude     float64         `json:"latitude"`
	Longitude    float64         `json:"longitude"`
	GeometryType string          `json:"geometry_type"`
	WaterDepth   *string         `json:"water_depth"`
	WaterLevelCM *int            `json:"water_level_cm"`
	Passability  *passabilityDTO `json:"passability"`
	Description  *string         `json:"description"`
	ImageKey     *string         `json:"image_key"`
	PeopleCount  *int            `json:"people_count"`
	HasChild     *bool           `json:"has_child"`
	HasElderly   *bool           `json:"has_elderly"`
	ContactPhone *string         `json:"contact_phone"`
	ClientID     *string         `json:"client_id"`
}

func (req createReportRequest) toDomain() domainreport.NewReportInput {
	in := domainreport.NewReportInput{
		Type:         domainreport.Type(req.Type),
		Severity:     domainreport.Severity(req.Severity),
		Latitude:     req.Latitude,
		Longitude:    req.Longitude,
		GeometryType: domainreport.GeometryType(req.GeometryType),
		WaterLevelCM: req.WaterLevelCM,
		Description:  req.Description,
		ImageKey:     req.ImageKey,
		PeopleCount:  req.PeopleCount,
		HasChild:     req.HasChild,
		HasElderly:   req.HasElderly,
		ContactPhone: req.ContactPhone,
		ClientID:     req.ClientID,
	}
	if req.WaterDepth != nil {
		d := domainreport.WaterDepth(*req.WaterDepth)
		in.WaterDepth = &d
	}
	if p := req.Passability; p != nil {
		in.Passability = &domainreport.Passability{
			Walk:       passOrUnknown(p.Walk),
			Motorcycle: passOrUnknown(p.Motorcycle),
			Sedan:      passOrUnknown(p.Sedan),
			SUVPickup:  passOrUnknown(p.SUVPickup),
		}
	}
	return in
}

// passOrUnknown lets clients omit vehicle classes they didn't assess.
func passOrUnknown(v string) domainreport.PassLevel {
	if v == "" {
		return domainreport.PassUnknown
	}
	return domainreport.PassLevel(v)
}

type confirmReportRequest struct {
	DeviceID string `json:"device_id"`
	Status   string `json:"status"`
}

func (req confirmReportRequest) toDomain() domainreport.NewConfirmationInput {
	return domainreport.NewConfirmationInput{
		DeviceID: req.DeviceID,
		Status:   domainreport.ConfirmationStatus(req.Status),
	}
}

type followResponse struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	ReportID  *string   `json:"report_id"`
	Latitude  *float64  `json:"latitude"`
	Longitude *float64  `json:"longitude"`
	RadiusM   *int      `json:"radius_m"`
	CreatedAt time.Time `json:"created_at"`
}

func toFollowResponse(f domainfollow.Follow) followResponse {
	var reportID *string
	if f.ReportID != nil {
		s := f.ReportID.String()
		reportID = &s
	}
	return followResponse{
		ID:        f.ID.String(),
		Kind:      string(f.Kind),
		ReportID:  reportID,
		Latitude:  f.Latitude,
		Longitude: f.Longitude,
		RadiusM:   f.RadiusM,
		CreatedAt: f.CreatedAt.UTC(),
	}
}

type createFollowRequest struct {
	DeviceID  string   `json:"device_id"`
	Kind      string   `json:"kind"`
	ReportID  *string  `json:"report_id"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	RadiusM   *int     `json:"radius_m"`
}

func (req createFollowRequest) toDomain() (domainfollow.NewFollowInput, bool) {
	in := domainfollow.NewFollowInput{
		DeviceID:  req.DeviceID,
		Kind:      domainfollow.Kind(req.Kind),
		Latitude:  req.Latitude,
		Longitude: req.Longitude,
		RadiusM:   req.RadiusM,
	}
	if req.ReportID != nil {
		id, err := uuid.Parse(*req.ReportID)
		if err != nil {
			return in, false
		}
		in.ReportID = &id
	}
	return in, true
}

type notificationResponse struct {
	ID        int64          `json:"id"`
	Kind      string         `json:"kind"`
	CreatedAt time.Time      `json:"created_at"`
	FollowID  string         `json:"follow_id"`
	Report    reportResponse `json:"report"`
}

type placeResponse struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

type presignRequest struct {
	ContentType   string `json:"content_type"`
	ContentLength int64  `json:"content_length"`
}

type presignResponse struct {
	ObjectKey string `json:"object_key"`
	UploadURL string `json:"upload_url"`
	ExpiresIn int64  `json:"expires_in"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

type aggregateCellResponse struct {
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	Count          int       `json:"count"`
	SevereCount    int       `json:"severe_count"`
	MaxSeverity    string    `json:"max_severity"`
	LatestUpdateAt time.Time `json:"latest_update_at"`
}

type aggregateResponse struct {
	Cells       []aggregateCellResponse `json:"cells"`
	CellSizeDeg float64                 `json:"cell_size_deg"`
	Total       int                     `json:"total"`
}

// --- Saved places ---

type areaSummaryResponse struct {
	Level          string     `json:"level"`
	ActiveCount    int        `json:"active_count"`
	SevereCount    int        `json:"severe_count"`
	LatestUpdateAt *time.Time `json:"latest_update_at"`
}

type savedPlaceResponse struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Icon                string              `json:"icon"`
	Latitude            float64             `json:"latitude"`
	Longitude           float64             `json:"longitude"`
	WatchRadiusM        int                 `json:"watch_radius_m"`
	PreferredVehicle    *string             `json:"preferred_vehicle"`
	NotificationEnabled bool                `json:"notification_enabled"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	Area                areaSummaryResponse `json:"area"`
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toSavedPlaceResponse(p domainfollow.PlaceWithSummary) savedPlaceResponse {
	return savedPlaceResponse{
		ID:                  p.ID.String(),
		Name:                deref(p.Name),
		Icon:                string(deref(p.Icon)),
		Latitude:            deref(p.Latitude),
		Longitude:           deref(p.Longitude),
		WatchRadiusM:        deref(p.RadiusM),
		PreferredVehicle:    p.PreferredVehicle,
		NotificationEnabled: p.Notify,
		CreatedAt:           p.CreatedAt.UTC(),
		UpdatedAt:           p.UpdatedAt.UTC(),
		Area: areaSummaryResponse{
			Level:          string(p.Summary.Level()),
			ActiveCount:    p.Summary.ActiveCount,
			SevereCount:    p.Summary.SevereCount,
			LatestUpdateAt: utcPtr(p.Summary.LatestUpdateAt),
		},
	}
}

type savedPlaceRequest struct {
	DeviceID            string   `json:"device_id"`
	Name                *string  `json:"name"`
	Icon                *string  `json:"icon"`
	Latitude            *float64 `json:"latitude"`
	Longitude           *float64 `json:"longitude"`
	WatchRadiusM        *int     `json:"watch_radius_m"`
	PreferredVehicle    *string  `json:"preferred_vehicle"`
	NotificationEnabled *bool    `json:"notification_enabled"`
}

func (req savedPlaceRequest) toDomain() domainfollow.PlaceFields {
	f := domainfollow.PlaceFields{
		Name:             req.Name,
		Latitude:         req.Latitude,
		Longitude:        req.Longitude,
		RadiusM:          req.WatchRadiusM,
		PreferredVehicle: req.PreferredVehicle,
		Notify:           req.NotificationEnabled,
	}
	if req.Icon != nil {
		i := domainfollow.PlaceIcon(*req.Icon)
		f.Icon = &i
	}
	return f
}

// --- Routes ---

type latLngDTO struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type evaluateRouteRequest struct {
	Origin      *latLngDTO `json:"origin"`
	Destination *latLngDTO `json:"destination"`
	Vehicle     string     `json:"vehicle"`
}

type geoJSONLineString struct {
	Type        string       `json:"type"`
	Coordinates [][2]float64 `json:"coordinates"`
}

type routeIncidentResponse struct {
	Report             reportResponse `json:"report"`
	DistanceFromRouteM float64        `json:"distance_from_route_m"`
	Impact             string         `json:"impact"`
	Reason             string         `json:"reason"`
}

type routeResponse struct {
	DistanceM     float64                 `json:"distance_m"`
	DurationS     float64                 `json:"duration_s"`
	Geometry      geoJSONLineString       `json:"geometry"`
	Risk          string                  `json:"risk"`
	IncidentCount int                     `json:"incident_count"`
	BlockingCount int                     `json:"blocking_count"`
	CautionCount  int                     `json:"caution_count"`
	Incidents     []routeIncidentResponse `json:"incidents"`
}

type evaluateRouteResponse struct {
	Vehicle      string          `json:"vehicle"`
	EvaluatedAt  time.Time       `json:"evaluated_at"`
	DataComplete bool            `json:"data_complete"`
	Routes       []routeResponse `json:"routes"`
}

// --- SOS / helpers ---

type sosEventResponse struct {
	Status    string    `json:"status"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

type sosHelperResponse struct {
	DisplayName  *string  `json:"display_name"`
	ContactPhone *string  `json:"contact_phone"`
	Capabilities []string `json:"capabilities"`
}

type sosResponse struct {
	ID                string             `json:"id"`
	Role              string             `json:"role"`
	Type              string             `json:"type"`
	Description       *string            `json:"description"`
	Latitude          float64            `json:"latitude"`
	Longitude         float64            `json:"longitude"`
	PeopleCount       *int               `json:"people_count"`
	ContactPhone      *string            `json:"contact_phone"`
	Status            string             `json:"status"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	ClosedAt          *time.Time         `json:"closed_at"`
	Events            []sosEventResponse `json:"events"`
	Helper            *sosHelperResponse `json:"helper"`
	NearbyHelperCount *int               `json:"nearby_helper_count"`
}

func capabilityStrings(caps []domainsos.Capability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	return out
}

type createSOSRequest struct {
	DeviceID     string  `json:"device_id"`
	ClientID     *string `json:"client_id"`
	Type         string  `json:"type"`
	Description  *string `json:"description"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	PeopleCount  *int    `json:"people_count"`
	ContactPhone *string `json:"contact_phone"`
}

type sosStatusRequest struct {
	DeviceID string `json:"device_id"`
	Status   string `json:"status"`
}

type sosDeviceRequest struct {
	DeviceID string `json:"device_id"`
}

type nearbySOSResponse struct {
	ID                   string    `json:"id"`
	Type                 string    `json:"type"`
	Description          *string   `json:"description"`
	ApproxLatitude       float64   `json:"approx_latitude"`
	ApproxLongitude      float64   `json:"approx_longitude"`
	DistanceM            float64   `json:"distance_m"`
	PeopleCount          *int      `json:"people_count"`
	CreatedAt            time.Time `json:"created_at"`
	RequiredCapabilities []string  `json:"required_capabilities"`
}

type helperResponse struct {
	Active       bool       `json:"active"`
	Capabilities []string   `json:"capabilities"`
	RadiusM      int        `json:"radius_m"`
	DisplayName  *string    `json:"display_name"`
	ContactPhone *string    `json:"contact_phone"`
	Latitude     *float64   `json:"latitude"`
	Longitude    *float64   `json:"longitude"`
	LocationAt   *time.Time `json:"location_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func toHelperResponse(h *domainsos.Helper) *helperResponse {
	if h == nil {
		return nil
	}
	return &helperResponse{
		Active:       h.Active,
		Capabilities: capabilityStrings(h.Capabilities),
		RadiusM:      h.RadiusM,
		DisplayName:  h.DisplayName,
		ContactPhone: h.ContactPhone,
		Latitude:     h.Latitude,
		Longitude:    h.Longitude,
		LocationAt:   utcPtr(h.LocationAt),
		UpdatedAt:    h.UpdatedAt.UTC(),
	}
}

type helperRequest struct {
	DeviceID     string   `json:"device_id"`
	Active       bool     `json:"active"`
	Capabilities []string `json:"capabilities"`
	RadiusM      int      `json:"radius_m"`
	DisplayName  *string  `json:"display_name"`
	ContactPhone *string  `json:"contact_phone"`
	Latitude     *float64 `json:"latitude"`
	Longitude    *float64 `json:"longitude"`
}

func (req helperRequest) toDomain() domainsos.HelperInput {
	caps := make([]domainsos.Capability, len(req.Capabilities))
	for i, c := range req.Capabilities {
		caps[i] = domainsos.Capability(c)
	}
	return domainsos.HelperInput{
		DeviceID: req.DeviceID, Active: req.Active, Capabilities: caps, RadiusM: req.RadiusM,
		DisplayName: req.DisplayName, ContactPhone: req.ContactPhone, Latitude: req.Latitude, Longitude: req.Longitude,
	}
}

// --- Important places ---

type importantPlaceResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	Address     *string   `json:"address"`
	Status      string    `json:"status"`
	Description *string   `json:"description"`
	Contact     *string   `json:"contact"`
	Source      *string   `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toImportantPlaceResponse(p domainplace.Place) importantPlaceResponse {
	return importantPlaceResponse{
		ID: p.ID.String(), Name: p.Name, Category: string(p.Category), Latitude: p.Latitude, Longitude: p.Longitude,
		Address: p.Address, Status: string(p.Status), Description: p.Description, Contact: p.Contact, Source: p.Source,
		CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(),
	}
}

type importantPlaceRequest struct {
	Name        *string  `json:"name"`
	Category    *string  `json:"category"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	Address     *string  `json:"address"`
	Status      *string  `json:"status"`
	Description *string  `json:"description"`
	Contact     *string  `json:"contact"`
	Source      *string  `json:"source"`
}

func (req importantPlaceRequest) toDomain() domainplace.Fields {
	f := domainplace.Fields{
		Name: req.Name, Latitude: req.Latitude, Longitude: req.Longitude, Address: req.Address,
		Description: req.Description, Contact: req.Contact, Source: req.Source,
	}
	if req.Category != nil {
		c := domainplace.Category(*req.Category)
		f.Category = &c
	}
	if req.Status != nil {
		s := domainplace.Status(*req.Status)
		f.Status = &s
	}
	return f
}

// --- Announcements ---

type announcementResponse struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Type        string     `json:"type"`
	Severity    string     `json:"severity"`
	SourceName  string     `json:"source_name"`
	SourceURL   *string    `json:"source_url"`
	Latitude    *float64   `json:"latitude"`
	Longitude   *float64   `json:"longitude"`
	RadiusM     *int       `json:"radius_m"`
	StartsAt    time.Time  `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at"`
	Status      string     `json:"status"`
	PublishedAt *time.Time `json:"published_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func toAnnouncementResponse(a domainannouncement.Announcement, now time.Time) announcementResponse {
	return announcementResponse{
		ID: a.ID.String(), Title: a.Title, Body: a.Body, Type: string(a.Type), Severity: string(a.Severity),
		SourceName: a.SourceName, SourceURL: a.SourceURL, Latitude: a.Latitude, Longitude: a.Longitude, RadiusM: a.RadiusM,
		StartsAt: a.StartsAt.UTC(), EndsAt: utcPtr(a.EndsAt), Status: string(a.Status(now)),
		PublishedAt: utcPtr(a.PublishedAt), CreatedAt: a.CreatedAt.UTC(), UpdatedAt: a.UpdatedAt.UTC(),
	}
}

type announcementRequest struct {
	Title         *string    `json:"title"`
	Body          *string    `json:"body"`
	Type          *string    `json:"type"`
	Severity      *string    `json:"severity"`
	SourceName    *string    `json:"source_name"`
	SourceURL     *string    `json:"source_url"`
	Latitude      *float64   `json:"latitude"`
	Longitude     *float64   `json:"longitude"`
	RadiusM       *int       `json:"radius_m"`
	ClearLocation bool       `json:"clear_location"`
	StartsAt      *time.Time `json:"starts_at"`
	EndsAt        *time.Time `json:"ends_at"`
	ClearEndsAt   bool       `json:"clear_ends_at"`
	Publish       bool       `json:"publish"`
}

func (req announcementRequest) toDomain() domainannouncement.Fields {
	f := domainannouncement.Fields{
		Title: req.Title, Body: req.Body, SourceName: req.SourceName, SourceURL: req.SourceURL,
		Latitude: req.Latitude, Longitude: req.Longitude, RadiusM: req.RadiusM, ClearLocation: req.ClearLocation,
		StartsAt: req.StartsAt, EndsAt: req.EndsAt, ClearEndsAt: req.ClearEndsAt,
	}
	if req.Type != nil {
		t := domainannouncement.Type(*req.Type)
		f.Type = &t
	}
	if req.Severity != nil {
		s := domainreport.Severity(*req.Severity)
		f.Severity = &s
	}
	return f
}

// --- Moderation ---

type problemReportRequest struct {
	DeviceID string  `json:"device_id"`
	Reason   string  `json:"reason"`
	Details  *string `json:"details"`
}

type problemReportResponse struct {
	ID        string    `json:"id"`
	ReportID  string    `json:"report_id"`
	Reason    string    `json:"reason"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type adminReportResponse struct {
	reportResponse
	HiddenAt     *time.Time `json:"hidden_at"`
	HiddenReason *string    `json:"hidden_reason"`
}

type complaintResponse struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	Details   *string   `json:"details"`
	CreatedAt time.Time `json:"created_at"`
}

type reportEventResponse struct {
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

type moderationItemResponse struct {
	Report         adminReportResponse   `json:"report"`
	ComplaintCount int                   `json:"complaint_count"`
	Reasons        map[string]int        `json:"reasons"`
	LatestAt       time.Time             `json:"latest_at"`
	Complaints     []complaintResponse   `json:"complaints"`
	Events         []reportEventResponse `json:"events"`
}

type moderationActionRequest struct {
	Action string `json:"action"`
}

// --- Public config ---

type donationConfigResponse struct {
	PromptPayID   string  `json:"promptpay_id"`
	IDType        string  `json:"id_type"`
	RecipientName *string `json:"recipient_name"`
}

type publicConfigResponse struct {
	Donation *donationConfigResponse `json:"donation"`
	// GISTDAFlood is true when the official GISTDA flood layer is configured.
	GISTDAFlood bool `json:"gistda_flood"`
}

// --- Official GISTDA flood layer ---

type floodAreaProperties struct {
	Ref        int        `json:"ref"`
	ID         string     `json:"id,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

type floodAreaFeature struct {
	Type       string              `json:"type"`
	Geometry   floodAreaGeometry   `json:"geometry"`
	Properties floodAreaProperties `json:"properties"`
}

type floodAreaGeometry struct {
	Type        string                `json:"type"`
	Coordinates []domainflood.Polygon `json:"coordinates"`
}

type floodAreaCollection struct {
	Type     string             `json:"type"`
	Features []floodAreaFeature `json:"features"`
}

type floodLayerResponse struct {
	Source     string              `json:"source"`
	Period     string              `json:"period"`
	ObservedAt *time.Time          `json:"observed_at"`
	FetchedAt  time.Time           `json:"fetched_at"`
	Stale      bool                `json:"stale"`
	SourceURL  string              `json:"source_url"`
	HasMore    bool                `json:"has_more"`
	Areas      floodAreaCollection `json:"areas"`
}

func toFloodLayerResponse(l *appflood.Layer) floodLayerResponse {
	features := make([]floodAreaFeature, 0, len(l.Areas))
	for _, a := range l.Areas {
		features = append(features, floodAreaFeature{
			Type:       "Feature",
			Geometry:   floodAreaGeometry{Type: "MultiPolygon", Coordinates: a.Polygons},
			Properties: floodAreaProperties{Ref: a.Ref, ID: a.ID, ObservedAt: utcPtr(a.ObservedAt)},
		})
	}
	return floodLayerResponse{
		Source:     domainflood.SourceName,
		Period:     string(l.Period),
		ObservedAt: utcPtr(l.ObservedAt),
		FetchedAt:  l.FetchedAt.UTC(),
		Stale:      l.Stale,
		SourceURL:  domainflood.SourceURL,
		HasMore:    l.HasMore,
		Areas:      floodAreaCollection{Type: "FeatureCollection", Features: features},
	}
}
