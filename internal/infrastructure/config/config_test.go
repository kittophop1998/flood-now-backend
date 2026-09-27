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

func TestR2CleanupConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c, warning := loadR2Cleanup()
		if !c.Enabled || c.Cron != defaultR2CleanupCron || c.OrphanRetention != defaultR2OrphanRetention ||
			c.ResolvedRetention != defaultR2ResolvedImageRetention || c.BatchSize != defaultR2CleanupBatchSize || c.DryRun {
			t.Errorf("got %+v", c)
		}
		if warning != "" {
			t.Errorf("warning = %q, want none", warning)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		t.Setenv("R2_CLEANUP_ENABLED", "false")
		c, _ := loadR2Cleanup()
		if c.Enabled {
			t.Error("expected disabled")
		}
	})

	t.Run("dry run and custom values", func(t *testing.T) {
		t.Setenv("R2_CLEANUP_DRY_RUN", "true")
		t.Setenv("R2_ORPHAN_RETENTION_HOURS", "48")
		t.Setenv("R2_RESOLVED_IMAGE_RETENTION_DAYS", "14")
		t.Setenv("R2_CLEANUP_BATCH_SIZE", "25")
		t.Setenv("R2_CLEANUP_CRON", "*/15 * * * *")
		c, warning := loadR2Cleanup()
		if !c.DryRun || c.OrphanRetention != 48*time.Hour || c.ResolvedRetention != 14*24*time.Hour || c.BatchSize != 25 || c.Cron != "*/15 * * * *" {
			t.Errorf("got %+v", c)
		}
		if warning != "" {
			t.Errorf("warning = %q, want none", warning)
		}
	})

	t.Run("invalid cron falls back to default", func(t *testing.T) {
		t.Setenv("R2_CLEANUP_CRON", "not a cron expression")
		c, warning := loadR2Cleanup()
		if c.Cron != defaultR2CleanupCron {
			t.Errorf("cron = %q, want fallback to default", c.Cron)
		}
		if !strings.Contains(warning, "R2_CLEANUP_CRON") {
			t.Errorf("warning = %q, want mention of R2_CLEANUP_CRON", warning)
		}
	})

	t.Run("invalid durations and batch size fall back to defaults", func(t *testing.T) {
		t.Setenv("R2_ORPHAN_RETENTION_HOURS", "not-a-number")
		t.Setenv("R2_RESOLVED_IMAGE_RETENTION_DAYS", "0")
		t.Setenv("R2_CLEANUP_BATCH_SIZE", "-1")
		c, warning := loadR2Cleanup()
		if c.OrphanRetention != defaultR2OrphanRetention || c.ResolvedRetention != defaultR2ResolvedImageRetention || c.BatchSize != defaultR2CleanupBatchSize {
			t.Errorf("got %+v", c)
		}
		for _, want := range []string{"R2_ORPHAN_RETENTION_HOURS", "R2_RESOLVED_IMAGE_RETENTION_DAYS", "R2_CLEANUP_BATCH_SIZE"} {
			if !strings.Contains(warning, want) {
				t.Errorf("warning = %q, want mention of %q", warning, want)
			}
		}
	})
}

func TestLoadBootsWithR2CleanupMisconfigured(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("R2_CLEANUP_CRON", "garbage")
	t.Setenv("R2_CLEANUP_BATCH_SIZE", "not-a-number")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("R2 cleanup config must never block boot: %v", err)
	}
	if cfg.R2Cleanup.Cron != defaultR2CleanupCron || cfg.R2Cleanup.BatchSize != defaultR2CleanupBatchSize || cfg.R2CleanupWarning == "" {
		t.Errorf("r2cleanup = %+v warning = %q", cfg.R2Cleanup, cfg.R2CleanupWarning)
	}
}
