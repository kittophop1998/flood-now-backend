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

func TestGenericTypesAndInfoSeverity(t *testing.T) {
	for _, typ := range []Type{TypeTrafficNotice, TypeAccidentEmergency, TypePowerUtility, TypeServiceDisruption, TypeWaterRelease, TypeGeneral} {
		if !typ.Valid() {
			t.Errorf("%s should be valid", typ)
		}
	}
	if Type("flood").Valid() {
		t.Error("unknown type accepted")
	}
	info := SeverityInfo
	if err := (Fields{Severity: &info}).Validate(false); err != nil {
		t.Errorf("info severity rejected: %v", err)
	}
	bad := report.Severity("urgent")
	if err := (Fields{Severity: &bad}).Validate(false); err == nil {
		t.Error("unknown severity accepted")
	}
}

func TestImagesValidateAndApply(t *testing.T) {
	img := func(key string) Image { return Image{Key: key, Width: ptr(1200), Height: ptr(800)} }
	ok := []Image{img("announcements/2026/09/27/a.jpg"), img("announcements/2026/09/27/b.webp")}
	if err := (Fields{Images: &ok}).Validate(false); err != nil {
		t.Fatalf("valid images rejected: %v", err)
	}
	cases := map[string][]Image{
		"too many":       {img("announcements/1"), img("announcements/2"), img("announcements/3"), img("announcements/4"), img("announcements/5"), img("announcements/6")},
		"report key":     {img("reports/2026/09/27/a.jpg")},
		"url":            {img("https://evil.example/announcements/a.jpg")},
		"path escape":    {img("announcements/../secrets")},
		"duplicate":      {img("announcements/a.jpg"), img("announcements/a.jpg")},
		"bad dimensions": {{Key: "announcements/a.jpg", Width: ptr(0)}},
		"empty key":      {{Key: ""}},
		"padded key":     {{Key: " announcements/a.jpg"}},
	}
	for name, images := range cases {
		if err := (Fields{Images: &images}).Validate(false); err == nil {
			t.Errorf("%s: invalid images accepted", name)
		}
	}

	a := Announcement{StartsAt: now}
	if err := (Fields{Images: &ok}).Apply(&a); err != nil || len(a.Images) != 2 || a.Images[0].Key != ok[0].Key {
		t.Fatalf("images not applied in order: %v %+v", err, a.Images)
	}
	if err := (Fields{Title: ptr("x")}).Apply(&a); err != nil || len(a.Images) != 2 {
		t.Error("an update without images must keep them")
	}
	empty := []Image{}
	if err := (Fields{Images: &empty}).Apply(&a); err != nil || len(a.Images) != 0 {
		t.Error("an empty images list must clear them")
	}
}
