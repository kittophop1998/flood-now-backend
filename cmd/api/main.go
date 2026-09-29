// Command api starts the FloodNow HTTP API.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	inboundhttp "floodnow-api/internal/adapters/inbound/http"
	"floodnow-api/internal/adapters/outbound/dohtraffic"
	"floodnow-api/internal/adapters/outbound/geocoding"
	"floodnow-api/internal/adapters/outbound/gistda"
	"floodnow-api/internal/adapters/outbound/postgres"
	"floodnow-api/internal/adapters/outbound/routing"
	"floodnow-api/internal/adapters/outbound/security"
	"floodnow-api/internal/adapters/outbound/storage"
	"floodnow-api/internal/adapters/outbound/stripe"
	appannouncement "floodnow-api/internal/application/announcement"
	appauth "floodnow-api/internal/application/auth"
	appcctv "floodnow-api/internal/application/cctv"
	appevent "floodnow-api/internal/application/event"
	appfollow "floodnow-api/internal/application/follow"
	appimagecleanup "floodnow-api/internal/application/imagecleanup"
	appimportantplace "floodnow-api/internal/application/importantplace"
	applocalservice "floodnow-api/internal/application/localservice"
	appmoderation "floodnow-api/internal/application/moderation"
	appofficialflood "floodnow-api/internal/application/officialflood"
	appplace "floodnow-api/internal/application/place"
	appreport "floodnow-api/internal/application/report"
	approute "floodnow-api/internal/application/route"
	appsos "floodnow-api/internal/application/sos"
	appupload "floodnow-api/internal/application/upload"
	"floodnow-api/internal/domain/donation"
	domainimagecleanup "floodnow-api/internal/domain/imagecleanup"
	domainlocalservice "floodnow-api/internal/domain/localservice"
	"floodnow-api/internal/domain/moderation"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/infrastructure/clock"
	"floodnow-api/internal/infrastructure/config"

	"github.com/robfig/cron/v3"
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
	if cfg.GISTDAWarning != "" {
		log.Printf("GISTDA config: %s", cfg.GISTDAWarning)
	}
	if !cfg.GISTDA.Enabled {
		log.Printf("GISTDA flood layer disabled (needs GISTDA_ENABLED=true, GISTDA_BASE_URL and GISTDA_API_KEY)")
	}
	if cfg.DOHCCTVWarning != "" {
		log.Printf("DOH CCTV config: %s", cfg.DOHCCTVWarning)
	}
	if !cfg.DOHCCTV.Enabled {
		log.Printf("DOH CCTV layer disabled (needs DOH_CCTV_ENABLED=true and a valid DOH_CCTV_BASE_URL)")
	}
	if cfg.AdminToken == "" {
		log.Printf("admin API disabled (ADMIN_TOKEN not set)")
	}
	if cfg.LocalServicesWarning != "" {
		log.Printf("local services config: %s", cfg.LocalServicesWarning)
	}
	if cfg.R2CleanupWarning != "" {
		log.Printf("R2 image cleanup config: %s", cfg.R2CleanupWarning)
	}

	realClock := clock.Real{}
	reportRepo := postgres.NewReportRepository(db)
	reportService := appreport.NewService(reportRepo, realClock, policy)
	followRepo := postgres.NewFollowRepository(db)
	followService := appfollow.NewService(followRepo, reportRepo, realClock)
	authService := appauth.NewService(postgres.NewUserRepository(db), followRepo, security.Bcrypt{}, realClock)
	eventService := appevent.NewService(postgres.NewEventRepository(db), realClock)
	placeService := appplace.NewService(geocoding.NewNominatim(cfg.GeocoderURL, cfg.GeocoderUserAgent))

	presigner := storage.NewR2Presigner(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Endpoint, cfg.R2Bucket)
	uploadService := appupload.NewService(presigner, realClock)

	routeService := approute.NewService(routing.NewOSRM(cfg.RouterURL, cfg.RouterFootURL, cfg.GeocoderUserAgent), reportRepo, realClock)
	sosService := appsos.NewService(postgres.NewSOSRepository(db), realClock)
	importantPlaceService := appimportantplace.NewService(postgres.NewImportantPlaceRepository(db), realClock)
	announcementRepo := postgres.NewAnnouncementRepository(db)
	announcementService := appannouncement.NewService(announcementRepo, realClock)
	moderationService := appmoderation.NewService(postgres.NewModerationRepository(db), reportRepo, realClock, modPolicy)

	// nil when local services are off: their routes don't exist and
	// /config/public reports local_services: null.
	localServiceService, err := newLocalServiceService(cfg.LocalServices, db, realClock)
	if err != nil {
		return err
	}
	var localServiceHandler *inboundhttp.LocalServiceHandler
	if localServiceService != nil {
		localServiceHandler = inboundhttp.NewLocalServiceHandler(localServiceService, cfg.ImageKitBaseURL, storage.ImageURL)
	}

	// nil when the cleanup job is disabled: no cron entry is registered and
	// the API is otherwise unaffected.
	var imageCleanupCron *cron.Cron
	if cfg.R2Cleanup.Enabled {
		cleanupPolicy := domainimagecleanup.Policy{
			Enabled:           true,
			OrphanRetention:   cfg.R2Cleanup.OrphanRetention,
			ResolvedRetention: cfg.R2Cleanup.ResolvedRetention,
			BatchSize:         cfg.R2Cleanup.BatchSize,
			DryRun:            cfg.R2Cleanup.DryRun,
		}
		if err := cleanupPolicy.Validate(); err != nil {
			log.Printf("R2 image cleanup disabled: invalid policy: %v", err)
		} else {
			cleanupService := appimagecleanup.NewService(reportRepo, announcementRepo, presigner, realClock, cleanupPolicy, log.Printf)
			imageCleanupCron = cron.New()
			if _, err := imageCleanupCron.AddFunc(cfg.R2Cleanup.Cron, func() {
				runImageCleanup(context.Background(), db, cleanupService)
			}); err != nil {
				// config.Load already validates the cron spec, so this would
				// mean the two parsers disagree — extremely unlikely, but
				// never worth failing boot over.
				log.Printf("R2 image cleanup disabled: invalid cron schedule %q: %v", cfg.R2Cleanup.Cron, err)
				imageCleanupCron = nil
			} else {
				imageCleanupCron.Start()
				log.Printf("R2 image cleanup scheduled: cron=%q dry_run=%v", cfg.R2Cleanup.Cron, cleanupPolicy.DryRun)
			}
		}
	} else {
		log.Printf("R2 image cleanup disabled (R2_CLEANUP_ENABLED=false)")
	}

	// nil when GISTDA isn't configured: the layer endpoint answers 404 and
	// /config/public reports it off.
	var floodService *appofficialflood.Service
	if cfg.GISTDA.Enabled {
		provider := gistda.New(cfg.GISTDA.BaseURL, cfg.GISTDA.APIKey, 25*time.Second, realClock)
		floodService = appofficialflood.NewService(provider, realClock, cfg.GISTDA.CacheTTL)
	}

	// nil when the camera layer is off: its endpoints answer 404 and
	// /config/public reports it off.
	var cctvService *appcctv.Service
	if cfg.DOHCCTV.Enabled {
		cctvService = appcctv.NewService(dohtraffic.New(cfg.DOHCCTV.BaseURL, 20*time.Second, realClock), realClock, cfg.DOHCCTV.CacheTTL)
	}

	sessionCookie := inboundhttp.SessionCookie{Name: cfg.SessionCookieName, Secure: cfg.SessionCookieSecure, SameSite: cfg.SessionCookieSameSite}
	if !cfg.Production {
		log.Printf("APP_ENV=development: plain http allowed, session cookie Secure=%v", cfg.SessionCookieSecure)
	}

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
		AnnouncementHandler:   inboundhttp.NewAnnouncementHandler(announcementService, realClock, cfg.ImageKitBaseURL, storage.ImageURL),
		ModerationHandler:     inboundhttp.NewModerationHandler(moderationService, presenter),
		ConfigHandler:         inboundhttp.NewConfigHandler(donationCfg, floodService != nil, cctvService != nil).WithLocalServices(localServiceService),
		OfficialFloodHandler:  inboundhttp.NewOfficialFloodHandler(floodService),
		CCTVHandler:           inboundhttp.NewCCTVHandler(cctvService),
		AuthHandler:           inboundhttp.NewAuthHandler(authService, sessionCookie),
		EventHandler:          inboundhttp.NewEventHandler(eventService, realClock, cfg.ImageKitBaseURL, storage.ImageURL),
		LocalServiceHandler:   localServiceHandler,
		AuthService:           authService,
		SessionCookie:         sessionCookie,
		WebOrigins:            cfg.WebOrigins,
		RequireHTTPS:          cfg.Production,
		TrustedProxies:        cfg.TrustedProxies,
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

	if imageCleanupCron != nil {
		<-imageCleanupCron.Stop().Done() // let an in-flight run finish before we close the DB pool
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Println("shutting down")
	return srv.Shutdown(shutdownCtx)
}

// newLocalServiceService builds the local-services use cases, or nil when
// they're off. An invalid billing policy is a boot error (it is money);
// invalid top-up packages fall back to the defaults with a warning.
func newLocalServiceService(c config.LocalServicesConfig, db *sql.DB, clk clock.Real) (*applocalservice.Service, error) {
	if !c.Enabled {
		log.Printf("local services disabled (SERVICE_PROVIDER_ENABLED is not true)")
		return nil, nil
	}
	policy := domainlocalservice.BillingPolicy{
		CreditEnabled: c.CreditEnabled, MatchFee: c.MatchFee, WelcomeCredit: c.WelcomeCredit,
		ConfirmTimeout: c.ConfirmTimeout, RequestTTL: c.RequestTTL, RefundGrace: c.RefundGrace,
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("invalid local services config: %w", err)
	}
	pkgs, err := domainlocalservice.ParsePackages(c.Packages)
	if err != nil {
		log.Printf("PROVIDER_TOPUP_PACKAGES invalid (%v); using defaults", err)
		pkgs = domainlocalservice.DefaultPackages
	}
	svcCfg := applocalservice.Config{Policy: policy, Packages: pkgs}
	if c.StripeEnabled {
		svcCfg.Payments = stripe.New(c.StripeAPIBase, c.StripeSecretKey, c.StripeWebhookSecret, 8*time.Second)
	}
	log.Printf("local services enabled: credit=%v match_fee=%d promptpay_topups=%v", policy.CreditEnabled, policy.MatchFee, svcCfg.Payments != nil)
	return applocalservice.NewService(postgres.NewLocalServiceRepository(db), clk, svcCfg), nil
}

// runImageCleanup takes the cross-replica advisory lock before running one
// cleanup pass, so at most one replica does this at a time; if another
// replica already holds it, this run is skipped entirely (safe: nothing is
// lost, the next scheduled run tries again).
func runImageCleanup(ctx context.Context, db *sql.DB, svc *appimagecleanup.Service) {
	release, ok, err := postgres.TryAdvisoryLock(ctx, db)
	if err != nil {
		log.Printf("R2 image cleanup: could not acquire advisory lock: %v", err)
		return
	}
	defer release()
	if !ok {
		log.Printf("R2 image cleanup: another instance is already running, skipping this cycle")
		return
	}
	svc.Run(ctx)
}
