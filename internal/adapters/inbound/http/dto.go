package http

import (
	"time"

	domainreport "floodnow-api/internal/domain/report"
)

type reportResponse struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	Severity         string    `json:"severity"`
	Latitude         float64   `json:"latitude"`
	Longitude        float64   `json:"longitude"`
	WaterLevelCM     *int      `json:"water_level_cm"`
	Description      *string   `json:"description"`
	ImageKey         *string   `json:"image_key"`
	ImageURL         *string   `json:"image_url"`
	PeopleCount      *int      `json:"people_count"`
	HasChild         *bool     `json:"has_child"`
	HasElderly       *bool     `json:"has_elderly"`
	ContactPhone     *string   `json:"contact_phone"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	LastVerifiedAt   time.Time `json:"last_verified_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	IsExpired        bool      `json:"is_expired"`
	StillActiveCount int       `json:"still_active_count"`
	ClearedCount     int       `json:"cleared_count"`
}

type listReportsResponse struct {
	Reports []reportResponse `json:"reports"`
}

type createReportRequest struct {
	Type         string  `json:"type"`
	Severity     string  `json:"severity"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	WaterLevelCM *int    `json:"water_level_cm"`
	Description  *string `json:"description"`
	ImageKey     *string `json:"image_key"`
	PeopleCount  *int    `json:"people_count"`
	HasChild     *bool   `json:"has_child"`
	HasElderly   *bool   `json:"has_elderly"`
	ContactPhone *string `json:"contact_phone"`
}

func (req createReportRequest) toDomain() domainreport.NewReportInput {
	return domainreport.NewReportInput{
		Type:         domainreport.Type(req.Type),
		Severity:     domainreport.Severity(req.Severity),
		Latitude:     req.Latitude,
		Longitude:    req.Longitude,
		WaterLevelCM: req.WaterLevelCM,
		Description:  req.Description,
		ImageKey:     req.ImageKey,
		PeopleCount:  req.PeopleCount,
		HasChild:     req.HasChild,
		HasElderly:   req.HasElderly,
		ContactPhone: req.ContactPhone,
	}
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
