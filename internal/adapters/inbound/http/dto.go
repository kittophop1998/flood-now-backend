package http

import (
	"time"

	"github.com/google/uuid"

	domainfollow "floodnow-api/internal/domain/follow"
	domainreport "floodnow-api/internal/domain/report"
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
