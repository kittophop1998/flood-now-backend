// Package config loads FloodNow API configuration from the environment.
package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	Port           string
	DatabaseURL    string
	WebOrigin      string
	ReportTTL      time.Duration

	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2Bucket          string
	R2Endpoint        string // optional override, derived from account id if empty

	ImageKitBaseURL string
}

func Load() (*Config, error) {
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
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	ttlStr := getEnv("REPORT_TTL", "2h")
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid REPORT_TTL %q: %w", ttlStr, err)
	}
	cfg.ReportTTL = ttl

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
