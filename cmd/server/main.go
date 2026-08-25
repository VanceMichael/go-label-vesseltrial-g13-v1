package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/auth"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/config"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/defect"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/delivery"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/httpapi"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/middleware"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/review"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/storage/sqlite"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/telemetry"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/trial"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	store, err := sqlite.Open(rootCtx, cfg.DatabasePath)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.EnsureJob(rootCtx, "telemetry_staleness_check", `{"max_age_seconds":120}`, 5, time.Now().UTC()); err != nil {
		logger.Error("could not initialize telemetry freshness worker", "error", err)
		os.Exit(1)
	}
	authService := auth.New(store, cfg.SessionTTL)
	reviewService := review.New(store)
	trialService := trial.New(store)
	telemetryService := telemetry.New(store)
	defectService := defect.New(store)
	deliveryService := delivery.New(store)
	api := httpapi.New(authService, reviewService, trialService, telemetryService, defectService, deliveryService, store, cfg.MaxRequestBytes)
	handler := middleware.RequestID(middleware.Recovery(logger, middleware.Logging(logger, api.Routes())))
	server := &http.Server{Addr: cfg.Address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	background := worker.New(store, logger, cfg.WorkerInterval)
	go background.Run(rootCtx)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("vessel trial server listening", "address", cfg.Address)
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case <-rootCtx.Done():
		logger.Info("shutdown requested")
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
