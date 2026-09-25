// Package config loads FloodNow API configuration from the environment.
package config

import (
	"fmt"
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

	if cfg.R2Endpoint == "" && cfg.R2AccountID != "" {
		cfg.R2Endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.R2AccountID)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
