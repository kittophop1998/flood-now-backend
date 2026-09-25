// Command api starts the FloodNow HTTP API.
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	appreport "floodnow-api/internal/application/report"
	appupload "floodnow-api/internal/application/upload"
	inboundhttp "floodnow-api/internal/adapters/inbound/http"
	"floodnow-api/internal/adapters/outbound/postgres"
	"floodnow-api/internal/adapters/outbound/storage"
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

	realClock := clock.Real{}
	reportRepo := postgres.NewReportRepository(db)
	reportService := appreport.NewService(reportRepo, realClock, cfg.ReportTTL)

	presigner := storage.NewR2Presigner(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Endpoint, cfg.R2Bucket)
	uploadService := appupload.NewService(presigner, realClock)

	reportHandler := inboundhttp.NewReportHandler(reportService, realClock, cfg.ImageKitBaseURL, storage.ImageURL)
	uploadHandler := inboundhttp.NewUploadHandler(uploadService)

	router := inboundhttp.NewRouter(inboundhttp.Deps{
		ReportHandler: reportHandler,
		UploadHandler: uploadHandler,
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
