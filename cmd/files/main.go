// Command files serves a workstation's workspace over HTTP: browse, download,
// upload, and preview images and video.
//
// It replaces a general-purpose file manager image with something small enough
// to audit. Two properties matter more than features here:
//
//   - Every path is resolved through os.Root, so a request cannot escape the
//     workspace directory even by way of a symlink placed inside it.
//   - Nothing is served inline unless its type is on a short preview allowlist.
//     App traffic is proxied on the controller's own origin, so rendering an
//     uploaded HTML file would run attacker-controlled script against the
//     controller's session cookie.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	listen         string
	root           string
	basePath       string
	maxUploadBytes int64
}

func loadConfig() (config, error) {
	c := config{
		listen: env("FILES_LISTEN", "0.0.0.0:7080"),
		root:   env("FILES_ROOT", "/workspace"),
		// Unset means "behind the controller's proxy"; explicitly empty means
		// "served at the root", which is how it runs standalone. Those are
		// different requests, so LookupEnv is used rather than env().
		basePath: "/apps/files",
	}
	if value, ok := os.LookupEnv("FILES_BASE_PATH"); ok {
		c.basePath = strings.TrimSpace(value)
	}
	c.basePath = strings.TrimSuffix(c.basePath, "/")
	raw := env("FILES_MAX_UPLOAD_BYTES", "5368709120") // 5 GiB
	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || size <= 0 {
		return config{}, fmt.Errorf("FILES_MAX_UPLOAD_BYTES must be a positive integer, got %q", raw)
	}
	c.maxUploadBytes = size
	if c.basePath != "" && !strings.HasPrefix(c.basePath, "/") {
		return config{}, fmt.Errorf("FILES_BASE_PATH must begin with /, got %q", c.basePath)
	}
	info, err := os.Stat(c.root)
	if err != nil {
		return config{}, fmt.Errorf("workspace root %s: %w", c.root, err)
	}
	if !info.IsDir() {
		return config{}, fmt.Errorf("workspace root %s is not a directory", c.root)
	}
	return c, nil
}

func main() {
	logger := slog.Default()
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	// os.Root pins the workspace for the process lifetime. Every subsequent
	// path operation goes through it, so confinement does not depend on any
	// individual handler remembering to check.
	root, err := os.OpenRoot(cfg.root)
	if err != nil {
		logger.Error("open workspace root", "error", err)
		os.Exit(1)
	}
	defer root.Close()

	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           newBrowser(cfg, root, logger).handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	logger.Info("workspace files ready",
		"listen", cfg.listen, "root", cfg.root, "base_path", cfg.basePath)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
