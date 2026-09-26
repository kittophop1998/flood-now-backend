package cctv

import (
	"math"
	"testing"
)

func TestNewDOHCameraValidates(t *testing.T) {
	cases := []struct {
		name, id, code string
		lat, lng       float64
		ok             bool
	}{
		{"valid", "1", "PER-1", 13.7, 100.5, true},
		{"no id", "", "PER-1", 13.7, 100.5, false},
		{"no code", "1", " ", 13.7, 100.5, false},
		{"no location", "1", "PER-1", 0, 0, false},
		{"lat out of range", "1", "PER-1", 91, 100.5, false},
		{"lng out of range", "1", "PER-1", 13.7, 181, false},
		{"nan", "1", "PER-1", math.NaN(), 100.5, false},
	}
	for _, tc := range cases {
		c, err := NewDOHCamera(tc.id, tc.code, tc.lat, tc.lng, " 1 ", "", "")
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		if err == nil && (c.HighwayNumber != "1" || c.Mode != ModeExternalLink || c.Status != StatusUnknown || c.Provider != ProviderDOH) {
			t.Errorf("%s: camera = %+v", tc.name, c)
		}
	}
}

func TestNearestOrdersAndLimits(t *testing.T) {
	a, _ := NewDOHCamera("a", "A", 13.7700, 100.5, "", "", "")
	b, _ := NewDOHCamera("b", "B", 13.7510, 100.5, "", "", "")
	c, _ := NewDOHCamera("c", "C", 13.7600, 100.5, "", "", "")
	cat := &Catalog{Cameras: []Camera{a, b, c}}
	got := cat.Nearest(13.75, 100.5, 1500, 2)
	if len(got) != 2 || got[0].ID != "doh-b" || got[1].ID != "doh-c" {
		t.Fatalf("nearest = %+v", got)
	}
	if d := got[0].DistanceM; d < 100 || d > 120 {
		t.Errorf("distance = %v, want ~111 m", d)
	}
}
