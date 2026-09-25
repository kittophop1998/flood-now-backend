// Package ports defines the boundaries the application layer depends on.
// Adapters implement these; domain/application never import an adapter.
package ports

import (
	"context"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/report"
)

// Clock is the only indirection over time.Now(), kept because expiry
// behavior needs to be deterministically testable.
type Clock interface {
	Now() time.Time
}

// ReportFilter narrows GET /reports to a map viewport and/or expired state.
type ReportFilter struct {
	BBox            *BBox
	IncludeExpired  bool
}

type BBox struct {
	MinLat, MaxLat, MinLng, MaxLng float64
}

// ConfirmParams is the atomic write the repository performs for a
// confirmation: upsert the (report_id, device_id) row, and — only when
// NewExpiry is non-nil (decided by the application layer's business rule,
// not the repository) — extend the parent report's freshness.
type ConfirmParams struct {
	ReportID  uuid.UUID
	DeviceID  string
	Status    report.ConfirmationStatus
	Now       time.Time
	NewExpiry *time.Time
}

type ReportRepository interface {
	Create(ctx context.Context, r *report.Report) error
	GetByID(ctx context.Context, id uuid.UUID) (*report.ReportWithStats, error)
	List(ctx context.Context, filter ReportFilter) ([]report.ReportWithStats, error)
	Confirm(ctx context.Context, params ConfirmParams) (*report.ReportWithStats, error)
}

// Presigner generates a time-limited direct-upload URL for object storage
// (Cloudflare R2). Implemented by the storage adapter.
type Presigner interface {
	PresignUpload(ctx context.Context, objectKey, contentType string, contentLength int64) (uploadURL string, expiresIn time.Duration, err error)
}
