package config

import (
	"strings"
	"testing"
	"time"
)

func setGISTDAEnv(t *testing.T, enabled, base, key, ttl string) {
	t.Setenv("GISTDA_ENABLED", enabled)
	t.Setenv("GISTDA_BASE_URL", base)
	t.Setenv("GISTDA_API_KEY", key)
	t.Setenv("GISTDA_CACHE_TTL_MINUTES", ttl)
}

func TestGISTDAConfig(t *testing.T) {
	const base = "https://api-gateway.gistda.or.th/api/2.0/resources"
	cases := []struct {
		name, enabled, base, key, ttl string
		wantEnabled                   bool
		wantTTL                       time.Duration
		wantWarning                   string
	}{
		{"off by default", "", "", "", "", false, 15 * time.Minute, ""},
		{"explicitly off", "false", base, "k", "", false, 15 * time.Minute, ""},
		{"fully configured", "true", base + "/", "k", "30", true, 30 * time.Minute, ""},
		{"missing key", "true", base, "", "", false, 15 * time.Minute, "GISTDA_API_KEY"},
		{"missing base url", "true", "", "k", "", false, 15 * time.Minute, "GISTDA_BASE_URL"},
		{"bad base url", "true", "api-gateway.gistda.or.th", "k", "", false, 15 * time.Minute, "GISTDA_BASE_URL"},
		{"bad ttl keeps layer on", "true", base, "k", "soon", true, 15 * time.Minute, "GISTDA_CACHE_TTL_MINUTES"},
		{"zero ttl rejected", "true", base, "k", "0", true, 15 * time.Minute, "GISTDA_CACHE_TTL_MINUTES"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setGISTDAEnv(t, tc.enabled, tc.base, tc.key, tc.ttl)
			g, warning := loadGISTDA()
			if g.Enabled != tc.wantEnabled || g.CacheTTL != tc.wantTTL {
				t.Errorf("enabled=%v ttl=%v", g.Enabled, g.CacheTTL)
			}
			if (tc.wantWarning == "") != (warning == "") || !strings.Contains(warning, tc.wantWarning) {
				t.Errorf("warning = %q, want mention of %q", warning, tc.wantWarning)
			}
			if tc.key != "" && strings.Contains(warning, tc.key+" ") {
				t.Errorf("warning must not echo the key: %q", warning)
			}
			if g.Enabled && strings.HasSuffix(g.BaseURL, "/") {
				t.Errorf("base url not trimmed: %q", g.BaseURL)
			}
		})
	}
}

func TestLoadBootsWithGISTDAMisconfigured(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	setGISTDAEnv(t, "true", "", "", "nope")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("GISTDA config must never block boot: %v", err)
	}
	if cfg.GISTDA.Enabled || cfg.GISTDAWarning == "" {
		t.Errorf("gistda = %+v warning = %q", cfg.GISTDA, cfg.GISTDAWarning)
	}
}

func TestDOHCCTVConfig(t *testing.T) {
	cases := []struct {
		name, enabled, base, ttl string
		wantEnabled              bool
		wantBase                 string
		wantTTL                  time.Duration
		wantWarning              string
	}{
		{"off by default", "", "", "", false, "https://highwaytraffic.go.th", time.Hour, ""},
		{"on with default url", "true", "", "", true, "https://highwaytraffic.go.th", time.Hour, ""},
		{"custom url and ttl", "true", "http://localhost:9000/", "30", true, "http://localhost:9000", 30 * time.Minute, ""},
		{"bad url disables", "true", "highwaytraffic.go.th", "", false, "highwaytraffic.go.th", time.Hour, "DOH_CCTV_BASE_URL"},
		{"bad ttl keeps layer on", "true", "", "often", true, "https://highwaytraffic.go.th", time.Hour, "DOH_CCTV_CACHE_TTL_MINUTES"},
		{"too short ttl rejected", "true", "", "1", true, "https://highwaytraffic.go.th", time.Hour, "DOH_CCTV_CACHE_TTL_MINUTES"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOH_CCTV_ENABLED", tc.enabled)
			t.Setenv("DOH_CCTV_BASE_URL", tc.base)
			t.Setenv("DOH_CCTV_CACHE_TTL_MINUTES", tc.ttl)
			d, warning := loadDOHCCTV()
			if d.Enabled != tc.wantEnabled || d.BaseURL != tc.wantBase || d.CacheTTL != tc.wantTTL {
				t.Errorf("got %+v", d)
			}
			if (tc.wantWarning == "") != (warning == "") || !strings.Contains(warning, tc.wantWarning) {
				t.Errorf("warning = %q, want mention of %q", warning, tc.wantWarning)
			}
		})
	}
}

func TestLoadBootsWithDOHCCTVMisconfigured(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("DOH_CCTV_ENABLED", "true")
	t.Setenv("DOH_CCTV_BASE_URL", "::bad::")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("DOH CCTV config must never block boot: %v", err)
	}
	if cfg.DOHCCTV.Enabled || cfg.DOHCCTVWarning == "" {
		t.Errorf("cctv = %+v warning = %q", cfg.DOHCCTV, cfg.DOHCCTVWarning)
	}
}
