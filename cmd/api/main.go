// Command api starts the FloodNow HTTP API.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	inboundhttp "floodnow-api/internal/adapters/inbound/http"
	"floodnow-api/internal/adapters/outbound/geocoding"
	"floodnow-api/internal/adapters/outbound/postgres"
	"floodnow-api/internal/adapters/outbound/storage"
	appfollow "floodnow-api/internal/application/follow"
	appplace "floodnow-api/internal/application/place"
	appreport "floodnow-api/internal/application/report"
	appupload "floodnow-api/internal/application/upload"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/infrastructure/clock"
	"floodnow-api/internal/infrastructure/config"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	policy := domainreport.FreshnessPolicy{
		StaleAfter:         cfg.ReportStaleAfter,
		TTL:                cfg.ReportTTL,
		FacilityStaleAfter: cfg.FacilityReportStaleAfter,
		FacilityTTL:        cfg.FacilityReportTTL,
		ResolveThreshold:   cfg.ReportResolveThreshold,
	}
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("invalid report lifecycle config: %w", err)
	}

	realClock := clock.Real{}
	reportRepo := postgres.NewReportRepository(db)
	reportService := appreport.NewService(reportRepo, realClock, policy)
	followService := appfollow.NewService(postgres.NewFollowRepository(db), reportRepo, realClock)
	placeService := appplace.NewService(geocoding.NewNominatim(cfg.GeocoderURL, cfg.GeocoderUserAgent))

	presigner := storage.NewR2Presigner(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Endpoint, cfg.R2Bucket)
	uploadService := appupload.NewService(presigner, realClock)

	presenter := inboundhttp.NewReportPresenter(realClock, cfg.ImageKitBaseURL, storage.ImageURL)

	router := inboundhttp.NewRouter(inboundhttp.Deps{
		ReportHandler: inboundhttp.NewReportHandler(reportService, presenter),
		UploadHandler: inboundhttp.NewUploadHandler(uploadService),
		FollowHandler: inboundhttp.NewFollowHandler(followService, presenter),
		PlaceHandler:  inboundhttp.NewPlaceHandler(placeService),
		WebOrigin:     cfg.WebOrigin,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("floodnow-api listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Println("shutting down")
	return srv.Shutdown(shutdownCtx)
}
