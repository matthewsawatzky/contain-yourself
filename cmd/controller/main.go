package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"workstation-manager/internal/config"
	"workstation-manager/internal/database"
	"workstation-manager/internal/httpapi"
	"workstation-manager/internal/workerclient"
)

func main() {
	_ = syscall.Umask(0o077)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadController()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	worker := workerclient.New(cfg.WorkerURL, cfg.WorkerToken)
	app, err := httpapi.New(cfg, db, worker, logger)
	if err != nil {
		logger.Error("controller startup failed", "error", err)
		os.Exit(1)
	}
	if cfg.ReconcileOnStartup {
		reconcileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := app.Reconcile(reconcileCtx); err != nil {
			logger.Warn("startup reconciliation deferred", "error", err)
		}
		cancel()
	}
	server := &http.Server{
		Addr: cfg.Listen, Handler: app.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 15 * time.Minute, IdleTimeout: 90 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("controller listening", "address", cfg.Listen, "database", cfg.DatabasePath)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("controller stopped", "error", err)
		os.Exit(1)
	}
}
