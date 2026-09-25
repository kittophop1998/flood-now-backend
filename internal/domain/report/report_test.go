package report

import "testing"

func TestNewReportInputValidate(t *testing.T) {
	valid := func() NewReportInput {
		return NewReportInput{
			Type:      TypeFlooded,
			Severity:  SeverityImpassable,
			Latitude:  13.75,
			Longitude: 100.5,
		}
	}

	t.Run("valid input passes", func(t *testing.T) {
		if err := valid().Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("invalid type rejected", func(t *testing.T) {
		in := valid()
		in.Type = "not-a-type"
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for invalid type")
		}
	})

	t.Run("invalid severity rejected", func(t *testing.T) {
		in := valid()
		in.Severity = "not-a-severity"
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for invalid severity")
		}
	})

	t.Run("out of range latitude rejected", func(t *testing.T) {
		in := valid()
		in.Latitude = 91
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for out-of-range latitude")
		}
	})

	t.Run("out of range longitude rejected", func(t *testing.T) {
		in := valid()
		in.Longitude = -181
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for out-of-range longitude")
		}
	})

	t.Run("negative water level rejected", func(t *testing.T) {
		in := valid()
		v := -5
		in.WaterLevelCM = &v
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for negative water level")
		}
	})

	t.Run("suspicious image key rejected", func(t *testing.T) {
		in := valid()
		key := "../../etc/passwd"
		in.ImageKey = &key
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for path-traversal image key")
		}
	})

	t.Run("plain image key accepted", func(t *testing.T) {
		in := valid()
		key := "reports/2026/09/25/abc.jpg"
		in.ImageKey = &key
		if err := in.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})
}

func TestConfirmationInputValidate(t *testing.T) {
	t.Run("valid confirmation passes", func(t *testing.T) {
		in := NewConfirmationInput{DeviceID: "12345678-aaaa-bbbb-cccc-dddddddddddd", Status: StatusStillActive}
		if err := in.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("invalid status rejected", func(t *testing.T) {
		in := NewConfirmationInput{DeviceID: "12345678-aaaa-bbbb-cccc-dddddddddddd", Status: "bogus"}
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for invalid status")
		}
	})

	t.Run("too-short device id rejected", func(t *testing.T) {
		in := NewConfirmationInput{DeviceID: "short", Status: StatusCleared}
		if err := in.Validate(); err == nil {
			t.Fatal("expected error for short device id")
		}
	})
}
