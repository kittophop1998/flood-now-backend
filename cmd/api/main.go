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
	"floodnow-api/internal/adapters/outbound/routing"
	"floodnow-api/internal/adapters/outbound/storage"
	appannouncement "floodnow-api/internal/application/announcement"
	appfollow "floodnow-api/internal/application/follow"
	appimportantplace "floodnow-api/internal/application/importantplace"
	appmoderation "floodnow-api/internal/application/moderation"
	appplace "floodnow-api/internal/application/place"
	appreport "floodnow-api/internal/application/report"
	approute "floodnow-api/internal/application/route"
	appsos "floodnow-api/internal/application/sos"
	appupload "floodnow-api/internal/application/upload"
	"floodnow-api/internal/domain/donation"
	"floodnow-api/internal/domain/moderation"
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

	modPolicy := moderation.Policy{
		AutoHideThreshold:   cfg.ModerationAutoHideThreshold,
		MaxPerDevicePerHour: cfg.ModerationMaxPerDeviceHour,
	}
	if err := modPolicy.Validate(); err != nil {
		return fmt.Errorf("invalid moderation config: %w", err)
	}

	donationCfg := donation.Load(cfg.DonationEnabled, cfg.PromptPayID, cfg.PromptPayName)
	if donationCfg == nil && cfg.DonationEnabled != "" && cfg.DonationEnabled != "false" {
		log.Printf("donation disabled: DONATION_ENABLED=%q but PROMPTPAY_ID is missing or invalid", cfg.DonationEnabled)
	}
	if cfg.AdminToken == "" {
		log.Printf("admin API disabled (ADMIN_TOKEN not set)")
	}

	realClock := clock.Real{}
	reportRepo := postgres.NewReportRepository(db)
	reportService := appreport.NewService(reportRepo, realClock, policy)
	followService := appfollow.NewService(postgres.NewFollowRepository(db), reportRepo, realClock)
	placeService := appplace.NewService(geocoding.NewNominatim(cfg.GeocoderURL, cfg.GeocoderUserAgent))

	presigner := storage.NewR2Presigner(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Endpoint, cfg.R2Bucket)
	uploadService := appupload.NewService(presigner, realClock)

	routeService := approute.NewService(routing.NewOSRM(cfg.RouterURL, cfg.RouterFootURL, cfg.GeocoderUserAgent), reportRepo, realClock)
	sosService := appsos.NewService(postgres.NewSOSRepository(db), realClock)
	importantPlaceService := appimportantplace.NewService(postgres.NewImportantPlaceRepository(db), realClock)
	announcementService := appannouncement.NewService(postgres.NewAnnouncementRepository(db), realClock)
	moderationService := appmoderation.NewService(postgres.NewModerationRepository(db), reportRepo, realClock, modPolicy)

	presenter := inboundhttp.NewReportPresenter(realClock, cfg.ImageKitBaseURL, storage.ImageURL)

	router := inboundhttp.NewRouter(inboundhttp.Deps{
		ReportHandler:         inboundhttp.NewReportHandler(reportService, presenter),
		UploadHandler:         inboundhttp.NewUploadHandler(uploadService),
		FollowHandler:         inboundhttp.NewFollowHandler(followService, presenter),
		PlaceHandler:          inboundhttp.NewPlaceHandler(placeService),
		SavedPlaceHandler:     inboundhttp.NewSavedPlaceHandler(followService),
		RouteHandler:          inboundhttp.NewRouteHandler(routeService, presenter),
		SOSHandler:            inboundhttp.NewSOSHandler(sosService),
		ImportantPlaceHandler: inboundhttp.NewImportantPlaceHandler(importantPlaceService),
		AnnouncementHandler:   inboundhttp.NewAnnouncementHandler(announcementService, realClock),
		ModerationHandler:     inboundhttp.NewModerationHandler(moderationService, presenter),
		ConfigHandler:         inboundhttp.NewConfigHandler(donationCfg),
		WebOrigin:             cfg.WebOrigin,
		AdminToken:            cfg.AdminToken,
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
