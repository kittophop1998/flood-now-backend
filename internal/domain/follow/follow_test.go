package follow

import (
	"testing"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/report"
)

const device = "12345678-aaaa-bbbb-cccc-dddddddddddd"

func ptr[T any](v T) *T { return &v }

func TestNewFollowInputValidate(t *testing.T) {
	ok := []NewFollowInput{
		{DeviceID: device, Kind: KindReport, ReportID: ptr(uuid.New())},
		{DeviceID: device, Kind: KindArea, Latitude: ptr(13.7), Longitude: ptr(100.5), RadiusM: ptr(3000)},
	}
	for _, in := range ok {
		if err := in.Validate(); err != nil {
			t.Errorf("%s follow: unexpected error %v", in.Kind, err)
		}
	}

	bad := map[string]NewFollowInput{
		"report follow without report": {DeviceID: device, Kind: KindReport},
		"area without center":          {DeviceID: device, Kind: KindArea, RadiusM: ptr(1000)},
		"area with unsupported radius": {DeviceID: device, Kind: KindArea, Latitude: ptr(13.7), Longitude: ptr(100.5), RadiusM: ptr(2500)},
		"unknown kind":                 {DeviceID: device, Kind: "city"},
		"short device id":              {DeviceID: "abc", Kind: KindReport, ReportID: ptr(uuid.New())},
	}
	for name, in := range bad {
		if err := in.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestNotificationKindFor(t *testing.T) {
	cases := []struct {
		follow   Kind
		event    report.EventKind
		severity report.Severity
		want     NotificationKind
		ok       bool
	}{
		{KindArea, report.EventCreated, report.SeverityCritical, NotifySevereNearby, true},
		{KindArea, report.EventCreated, report.SeverityModerate, "", false},
		{KindArea, report.EventResolved, report.SeverityCritical, "", false},
		{KindReport, report.EventConfirmed, report.SeverityLow, NotifyConfirmed, true},
		{KindReport, report.EventUpdated, report.SeverityLow, NotifyUpdated, true},
		{KindArea, report.EventUpdated, report.SeverityCritical, "", false},
		{KindReport, report.EventResolved, report.SeverityLow, NotifyResolved, true},
		{KindReport, report.EventReopened, report.SeverityLow, NotifyReopened, true},
		{KindReport, report.EventCreated, report.SeverityCritical, "", false},
	}
	for _, c := range cases {
		got, ok := NotificationKindFor(c.follow, c.event, c.severity)
		if got != c.want || ok != c.ok {
			t.Errorf("%s/%s/%s = (%q, %v), want (%q, %v)", c.follow, c.event, c.severity, got, ok, c.want, c.ok)
		}
	}
}

func TestPlaceNotificationsAreSevereOnlyAndIncludeReopened(t *testing.T) {
	if k, ok := NotificationKindFor(KindPlace, report.EventCreated, report.SeverityCritical); !ok || k != NotifySevereNearby {
		t.Error("a new critical report in a watched place notifies")
	}
	if k, ok := NotificationKindFor(KindPlace, report.EventReopened, report.SeverityHigh); !ok || k != NotifySevereNearby {
		t.Error("a severe report happening again in a watched place notifies")
	}
	if _, ok := NotificationKindFor(KindPlace, report.EventCreated, report.SeverityModerate); ok {
		t.Error("non-severe reports never notify a watched place")
	}
	if _, ok := NotificationKindFor(KindPlace, report.EventConfirmed, report.SeverityCritical); ok {
		t.Error("confirmations never notify a watched place (anti-spam)")
	}
}

func TestPlaceFieldsValidate(t *testing.T) {
	home := IconHome
	if err := (PlaceFields{Name: ptr("บ้าน"), Icon: &home, Latitude: ptr(13.7), Longitude: ptr(100.5)}).Validate(true); err != nil {
		t.Errorf("valid place rejected: %v", err)
	}
	bad := map[string]PlaceFields{
		"missing name":  {Icon: &home, Latitude: ptr(13.7), Longitude: ptr(100.5)},
		"blank name":    {Name: ptr("  "), Icon: &home, Latitude: ptr(13.7), Longitude: ptr(100.5)},
		"latitude only": {Name: ptr("x"), Icon: &home, Latitude: ptr(13.7)},
		"bad radius":    {Name: ptr("x"), Icon: &home, Latitude: ptr(13.7), Longitude: ptr(100.5), RadiusM: ptr(2500)},
		"bad vehicle":   {Name: ptr("x"), Icon: &home, Latitude: ptr(13.7), Longitude: ptr(100.5), PreferredVehicle: ptr("bus")},
		"out of range":  {Name: ptr("x"), Icon: &home, Latitude: ptr(95.0), Longitude: ptr(100.5)},
	}
	for name, p := range bad {
		if p.Validate(true) == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	summary := AreaSummary{ActiveCount: 2}
	if summary.Level() != AreaCaution || (AreaSummary{}).Level() != AreaClear || (AreaSummary{ActiveCount: 1, SevereCount: 1}).Level() != AreaSevere {
		t.Error("area level derivation is wrong")
	}
}
