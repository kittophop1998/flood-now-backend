package announcement

import (
	"testing"
	"time"

	"floodnow-api/internal/domain/report"
)

var now = time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func TestStatusIsDerivedFromPublicationAndWindow(t *testing.T) {
	a := Announcement{StartsAt: now.Add(-time.Hour)}
	if a.Status(now) != StatusDraft {
		t.Error("unpublished → draft")
	}
	a.PublishedAt = ptr(now.Add(-2 * time.Hour))
	if a.Status(now) != StatusActive {
		t.Error("published, started, no end → active")
	}
	a.EndsAt = ptr(now)
	if a.Status(now) != StatusExpired {
		t.Error("ends_at reached → expired")
	}
	a.EndsAt = nil
	a.StartsAt = now.Add(time.Hour)
	if a.Status(now) != StatusScheduled {
		t.Error("starts in the future → scheduled")
	}
}

func TestFieldsValidateAndApply(t *testing.T) {
	typ, sev := TypeFloodWarning, report.SeverityHigh
	full := Fields{
		Title: ptr("Flood warning"), Body: ptr("River rising"), Type: &typ, Severity: &sev,
		SourceName: ptr("Dept. of Water Resources"), SourceURL: ptr("https://example.go.th/a"), StartsAt: ptr(now),
	}
	if err := full.Validate(true); err != nil {
		t.Fatalf("valid announcement rejected: %v", err)
	}
	if err := (Fields{Title: ptr("x")}).Validate(true); err == nil {
		t.Error("create without required fields accepted")
	}
	if err := (Fields{SourceURL: ptr("javascript:alert(1)")}).Validate(false); err == nil {
		t.Error("non-http source_url accepted")
	}

	var a Announcement
	if err := full.Apply(&a); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := (Fields{RadiusM: ptr(500)}).Apply(&a); err == nil {
		t.Error("a radius without a point must be rejected")
	}
	a.RadiusM = nil
	if err := (Fields{EndsAt: ptr(now.Add(-time.Minute))}).Apply(&a); err == nil {
		t.Error("ends_at before starts_at must be rejected")
	}
}
