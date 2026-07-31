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
	"workstation-manager/internal/dockerworker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadWorker()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if !dockerworker.SocketExists(cfg.DockerSocket) {
		logger.Error("Docker socket is unavailable", "path", cfg.DockerSocket)
		os.Exit(1)
	}
	engine := dockerworker.NewEngine(cfg.DockerSocket)
	service := dockerworker.NewService(cfg, engine, logger)
	server := &http.Server{
		Addr: cfg.Listen, Handler: service.Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("worker listening", "address", cfg.Listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
