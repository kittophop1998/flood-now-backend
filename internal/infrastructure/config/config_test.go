package config

import (
	"net/http"
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
	t.Setenv("WEB_ORIGIN", "https://floodnow.example")
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
	t.Setenv("WEB_ORIGIN", "https://floodnow.example")
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
	t.Setenv("WEB_ORIGIN", "https://floodnow.example")
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

func TestWebSecurityConfig(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL": "postgres://example", "APP_ENV": "", "WEB_ORIGIN": "https://floodnow.example",
		"SESSION_COOKIE_NAME": "", "SESSION_COOKIE_SECURE": "", "SESSION_COOKIE_SAMESITE": "", "TRUSTED_PROXIES": "",
	}
	load := func(t *testing.T, env map[string]string) (*Config, error) {
		t.Helper()
		for k, v := range base {
			t.Setenv(k, v)
		}
		for k, v := range env {
			t.Setenv(k, v)
		}
		return Load()
	}

	t.Run("production defaults are strict", func(t *testing.T) {
		cfg, err := load(t, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Production || !cfg.SessionCookieSecure || cfg.SessionCookieSameSite != http.SameSiteStrictMode ||
			cfg.SessionCookieName != "__Host-floodnow_session" || len(cfg.WebOrigins) != 1 || cfg.WebOrigins[0] != "https://floodnow.example" {
			t.Errorf("unexpected production config: %+v", cfg)
		}
		if len(cfg.TrustedProxies) == 0 {
			t.Error("production must trust the platform's private-network proxy by default")
		}
	})

	t.Run("development allows http localhost", func(t *testing.T) {
		cfg, err := load(t, map[string]string{"APP_ENV": "development", "WEB_ORIGIN": ""})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Production || cfg.SessionCookieSecure || cfg.SessionCookieName != "floodnow_session" || cfg.WebOrigins[0] != "http://localhost:3000" {
			t.Errorf("unexpected development config: %+v", cfg)
		}
	})

	t.Run("several exact origins", func(t *testing.T) {
		cfg, err := load(t, map[string]string{"WEB_ORIGIN": "https://FloodNow.example/, https://staging.floodnow.example"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(cfg.WebOrigins, " ") != "https://floodnow.example https://staging.floodnow.example" {
			t.Errorf("origins = %v", cfg.WebOrigins)
		}
	})

	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing origin in production", map[string]string{"WEB_ORIGIN": ""}, "WEB_ORIGIN is required"},
		{"http origin in production", map[string]string{"WEB_ORIGIN": "http://floodnow.example"}, "must be https"},
		{"wildcard origin", map[string]string{"WEB_ORIGIN": "*"}, "not an exact"},
		{"wildcard subdomain", map[string]string{"WEB_ORIGIN": "https://*.floodnow.example"}, "not an exact"},
		{"origin with a path", map[string]string{"WEB_ORIGIN": "https://floodnow.example/app"}, "not an exact"},
		{"insecure cookie in production", map[string]string{"SESSION_COOKIE_SECURE": "false"}, "not allowed in production"},
		{"samesite none without secure", map[string]string{"APP_ENV": "development", "SESSION_COOKIE_SAMESITE": "none"}, "requires a Secure"},
		{"unknown samesite", map[string]string{"SESSION_COOKIE_SAMESITE": "loose"}, "SESSION_COOKIE_SAMESITE"},
		{"__Host- without secure", map[string]string{"APP_ENV": "development", "SESSION_COOKIE_NAME": "__Host-x"}, "needs SESSION_COOKIE_SECURE"},
		{"bad cookie name", map[string]string{"SESSION_COOKIE_NAME": "a;b"}, "SESSION_COOKIE_NAME"},
		{"bad trusted proxy", map[string]string{"TRUSTED_PROXIES": "10.0.0.0/33"}, "TRUSTED_PROXIES"},
		{"unknown env", map[string]string{"APP_ENV": "staging"}, "APP_ENV"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(t, tc.env); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}

	t.Run("trusted proxies can be narrowed or turned off", func(t *testing.T) {
		cfg, err := load(t, map[string]string{"TRUSTED_PROXIES": "10.1.2.3, fd00::/8"})
		if err != nil || strings.Join(cfg.TrustedProxies, " ") != "10.1.2.3 fd00::/8" {
			t.Errorf("proxies = %v, err %v", cfg.TrustedProxies, err)
		}
		cfg, err = load(t, map[string]string{"TRUSTED_PROXIES": "none"})
		if err != nil || cfg.TrustedProxies != nil {
			t.Errorf("proxies = %v, err %v", cfg.TrustedProxies, err)
		}
	})
}

func TestLocalServicesConfig(t *testing.T) {
	web := []string{"https://floodnow.example"}
	for _, k := range []string{"SERVICE_PROVIDER_ENABLED", "PROVIDER_CREDIT_ENABLED", "MATCH_FEE_CREDITS", "PROVIDER_WELCOME_CREDITS",
		"PROVIDER_CONFIRM_TIMEOUT", "SERVICE_REQUEST_TTL", "MATCH_REFUND_GRACE", "STRIPE_TOPUP_ENABLED", "STRIPE_SECRET_KEY",
		"STRIPE_WEBHOOK_SECRET", "STRIPE_RETURN_URL", "STRIPE_API_BASE", "PROVIDER_TOPUP_PACKAGES"} {
		t.Setenv(k, "")
	}

	c, warning := loadLocalServices(web, true)
	if c.Enabled || c.CreditEnabled || c.StripeEnabled || warning != "" {
		t.Fatalf("off by default: %+v %q", c, warning)
	}
	if c.MatchFee != 20 || c.ConfirmTimeout != 10*time.Minute || c.RequestTTL != 2*time.Hour || c.RefundGrace != 10*time.Minute {
		t.Fatalf("defaults: %+v", c)
	}
	if c.StripeReturnURL != "https://floodnow.example/" {
		t.Fatalf("return URL should default to the web origin, got %q", c.StripeReturnURL)
	}

	t.Setenv("SERVICE_PROVIDER_ENABLED", "true")
	t.Setenv("PROVIDER_CREDIT_ENABLED", "true")
	t.Setenv("MATCH_FEE_CREDITS", "-5")
	t.Setenv("PROVIDER_CONFIRM_TIMEOUT", "1s")
	t.Setenv("STRIPE_TOPUP_ENABLED", "true")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_secret_value")
	c, warning = loadLocalServices(web, true)
	if c.MatchFee != 20 || c.ConfirmTimeout != 10*time.Minute {
		t.Fatalf("invalid values fall back to defaults: %+v", c)
	}
	if c.StripeEnabled || !strings.Contains(warning, "STRIPE_WEBHOOK_SECRET") {
		t.Fatalf("top-ups need both secrets: enabled=%v warning=%q", c.StripeEnabled, warning)
	}
	if strings.Contains(warning, "sk_test_secret_value") {
		t.Fatalf("warning must not echo a secret: %q", warning)
	}

	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_x")
	t.Setenv("STRIPE_RETURN_URL", "http://floodnow.example/")
	if c, warning = loadLocalServices(web, true); c.StripeEnabled || !strings.Contains(warning, "STRIPE_RETURN_URL") {
		t.Fatalf("production return URL must be https: enabled=%v %q", c.StripeEnabled, warning)
	}
	if c, _ = loadLocalServices(web, false); !c.StripeEnabled {
		t.Fatal("http return URL is fine in development")
	}

	t.Setenv("PROVIDER_CREDIT_ENABLED", "false")
	if c, warning = loadLocalServices(web, false); c.StripeEnabled || !strings.Contains(warning, "PROVIDER_CREDIT_ENABLED") {
		t.Fatalf("top-ups need credit on: enabled=%v %q", c.StripeEnabled, warning)
	}
}
