// Package config loads FloodNow API configuration from the environment.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string
	DatabaseURL string
	WebOrigin   string
	// Report lifecycle timing (see domain/report.FreshnessPolicy).
	ReportStaleAfter         time.Duration
	ReportTTL                time.Duration
	FacilityReportStaleAfter time.Duration
	FacilityReportTTL        time.Duration
	ReportResolveThreshold   int

	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2Bucket          string
	R2Endpoint        string // optional override, derived from account id if empty

	ImageKitBaseURL string

	GeocoderURL       string
	GeocoderUserAgent string

	// OSRM-compatible routing for safe-route evaluation.
	RouterURL     string // serves /route/v1/driving
	RouterFootURL string // serves /route/v1/foot

	// AdminToken gates the operator API; empty disables it.
	AdminToken string

	// Moderation (see domain/moderation.Policy).
	ModerationAutoHideThreshold int
	ModerationMaxPerDeviceHour  int

	// Donation (PromptPay). Validated/normalized in domain/donation; any
	// problem just turns the feature off.
	DonationEnabled string
	PromptPayID     string
	PromptPayName   string

	// Official GISTDA flood-area layer. Enabled only when GISTDA_ENABLED=true
	// and the base URL/key are valid; otherwise the layer is off and
	// GISTDAWarning says why (the rest of the API is unaffected).
	GISTDA        GISTDAConfig
	GISTDAWarning string
}

type GISTDAConfig struct {
	Enabled  bool
	BaseURL  string
	APIKey   string // secret: never logged or sent to clients
	CacheTTL time.Duration
}

// Load reads configuration from the environment, loading a .env file first
// if one is present (local dev only — in production, real env vars are set
// by the platform and no .env file exists, so a missing file is not an error).
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		Port:        getEnv("PORT", "4000"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		WebOrigin:   getEnv("WEB_ORIGIN", "http://localhost:3000"),

		R2AccountID:       os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket:          os.Getenv("R2_BUCKET"),
		R2Endpoint:        os.Getenv("R2_ENDPOINT"),

		ImageKitBaseURL: os.Getenv("IMAGEKIT_BASE_URL"),

		GeocoderURL:       strings.TrimRight(getEnv("GEOCODER_URL", "https://nominatim.openstreetmap.org"), "/"),
		GeocoderUserAgent: getEnv("GEOCODER_USER_AGENT", "FloodNow/1.0 (community flood map)"),

		RouterURL:     strings.TrimRight(getEnv("ROUTER_URL", "https://routing.openstreetmap.de/routed-car"), "/"),
		RouterFootURL: strings.TrimRight(getEnv("ROUTER_FOOT_URL", "https://routing.openstreetmap.de/routed-foot"), "/"),

		AdminToken: os.Getenv("ADMIN_TOKEN"),

		DonationEnabled: os.Getenv("DONATION_ENABLED"),
		PromptPayID:     os.Getenv("PROMPTPAY_ID"),
		PromptPayName:   os.Getenv("PROMPTPAY_NAME"),
	}
	if cfg.AdminToken != "" && len(cfg.AdminToken) < 24 {
		return nil, fmt.Errorf("ADMIN_TOKEN must be at least 24 characters (or empty to disable the admin API)")
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	durations := []struct {
		key, fallback string
		dst           *time.Duration
	}{
		{"REPORT_STALE_AFTER", "2h", &cfg.ReportStaleAfter},
		{"REPORT_TTL", "6h", &cfg.ReportTTL},
		{"FACILITY_REPORT_STALE_AFTER", "12h", &cfg.FacilityReportStaleAfter},
		{"FACILITY_REPORT_TTL", "48h", &cfg.FacilityReportTTL},
	}
	for _, d := range durations {
		raw := getEnv(d.key, d.fallback)
		v, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", d.key, raw, err)
		}
		*d.dst = v
	}

	thresholdStr := getEnv("REPORT_RESOLVE_THRESHOLD", "2")
	threshold, err := strconv.Atoi(thresholdStr)
	if err != nil {
		return nil, fmt.Errorf("invalid REPORT_RESOLVE_THRESHOLD %q: %w", thresholdStr, err)
	}
	cfg.ReportResolveThreshold = threshold

	ints := []struct {
		key, fallback string
		dst           *int
	}{
		{"MODERATION_AUTO_HIDE_THRESHOLD", "3", &cfg.ModerationAutoHideThreshold},
		{"MODERATION_MAX_PER_DEVICE_HOUR", "10", &cfg.ModerationMaxPerDeviceHour},
	}
	for _, it := range ints {
		raw := getEnv(it.key, it.fallback)
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", it.key, raw, err)
		}
		*it.dst = v
	}

	cfg.GISTDA, cfg.GISTDAWarning = loadGISTDA()

	if cfg.R2Endpoint == "" && cfg.R2AccountID != "" {
		cfg.R2Endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.R2AccountID)
	}

	return cfg, nil
}

const defaultGISTDACacheTTL = 15 * time.Minute

// loadGISTDA never fails the boot: any missing or invalid value disables
// only the GISTDA layer, with a warning for the log.
func loadGISTDA() (GISTDAConfig, string) {
	g := GISTDAConfig{
		BaseURL:  strings.TrimRight(strings.TrimSpace(os.Getenv("GISTDA_BASE_URL")), "/"),
		APIKey:   strings.TrimSpace(os.Getenv("GISTDA_API_KEY")),
		CacheTTL: defaultGISTDACacheTTL,
	}
	var warnings []string
	if raw := os.Getenv("GISTDA_CACHE_TTL_MINUTES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 24*60 {
			warnings = append(warnings, fmt.Sprintf("GISTDA_CACHE_TTL_MINUTES=%q is invalid (1–1440), using 15", raw))
		} else {
			g.CacheTTL = time.Duration(n) * time.Minute
		}
	}
	if os.Getenv("GISTDA_ENABLED") != "true" {
		return g, strings.Join(warnings, "; ")
	}
	u, err := url.Parse(g.BaseURL)
	urlOK := g.BaseURL != "" && err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
	if !urlOK {
		warnings = append(warnings, "GISTDA_BASE_URL is missing or not an http(s) URL")
	}
	if g.APIKey == "" {
		warnings = append(warnings, "GISTDA_API_KEY is missing")
	}
	// A bad TTL only falls back to the default; it doesn't disable the layer.
	g.Enabled = urlOK && g.APIKey != ""
	return g, strings.Join(warnings, "; ")
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
