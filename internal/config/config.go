// Package config loads and validates process configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Controller struct {
	Listen                 string
	DatabasePath           string
	AppsDirectory          string
	InstalledAppsDirectory string
	AppStoreDirectory      string
	AppStoreIndexURL       string
	ControllerVersion      string
	TemplatesDirectory     string
	VPNProfilesDirectory   string
	VPNEncryptionKeyFile   string
	WorkerURL              string
	WorkerToken            string
	PublicBaseDomain       string
	SecureCookies          bool
	SessionLifetime        time.Duration
	DefaultCPU             float64
	DefaultMemoryMB        int
	DefaultPIDLimit        int
	ReconcileOnStartup     bool
}

func LoadController() (Controller, error) {
	c := Controller{
		Listen:                 env("CONTROLLER_LISTEN", "0.0.0.0:8080"),
		DatabasePath:           env("DATABASE_PATH", "/data/controller.db"),
		AppsDirectory:          env("APPS_DIRECTORY", "/config/apps"),
		InstalledAppsDirectory: env("INSTALLED_APPS_DIRECTORY", "/data/apps"),
		AppStoreDirectory:      env("APP_STORE_DIRECTORY", "/data/app-store"),
		AppStoreIndexURL:       env("APP_STORE_INDEX_URL", "https://raw.githubusercontent.com/matthewsawatzky/contain-yourself/main/app_store/index.json"),
		ControllerVersion:      strings.TrimPrefix(env("CONTROLLER_VERSION", "0.3.1"), "v"),
		TemplatesDirectory:     env("TEMPLATES_DIRECTORY", "/config/templates"),
		VPNProfilesDirectory:   env("VPN_PROFILES_DIRECTORY", "/data/vpn-profiles"),
		VPNEncryptionKeyFile:   env("VPN_ENCRYPTION_KEY_FILE", "/data/vpn-profiles.key"),
		WorkerURL:              strings.TrimRight(env("WORKER_URL", "http://docker-worker:8090"), "/"),
		WorkerToken:            os.Getenv("WORKER_TOKEN"),
		PublicBaseDomain:       strings.ToLower(strings.TrimSpace(os.Getenv("PUBLIC_BASE_DOMAIN"))),
		SecureCookies:          envBool("SECURE_COOKIES", false),
		SessionLifetime:        envDuration("SESSION_LIFETIME", 24*time.Hour),
		DefaultCPU:             envFloat("DEFAULT_CPU", 2),
		DefaultMemoryMB:        envInt("DEFAULT_MEMORY_MB", 4096),
		DefaultPIDLimit:        envInt("DEFAULT_PID_LIMIT", 512),
		ReconcileOnStartup:     envBool("RECONCILE_ON_STARTUP", true),
	}
	if c.WorkerToken == "" {
		return Controller{}, errors.New("WORKER_TOKEN is required")
	}
	if len(c.WorkerToken) < 24 {
		return Controller{}, errors.New("WORKER_TOKEN must contain at least 24 characters")
	}
	if c.SessionLifetime < 5*time.Minute {
		return Controller{}, errors.New("SESSION_LIFETIME must be at least 5m")
	}
	if c.DefaultCPU <= 0 || c.DefaultMemoryMB < 128 || c.DefaultPIDLimit < 32 {
		return Controller{}, errors.New("default resource limits are invalid")
	}
	u, err := url.Parse(c.WorkerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Controller{}, fmt.Errorf("invalid WORKER_URL %q", c.WorkerURL)
	}
	return c, nil
}

type Worker struct {
	Listen            string
	Token             string
	DockerSocket      string
	ManagementNetwork string
	AllowedImages     map[string]struct{}
	WSLANImage        string
	ApprovalsPath     string
}

func LoadWorker() (Worker, error) {
	w := Worker{
		Listen:            env("WORKER_LISTEN", "0.0.0.0:8090"),
		Token:             os.Getenv("WORKER_TOKEN"),
		DockerSocket:      env("DOCKER_SOCKET", "/var/run/docker.sock"),
		ManagementNetwork: env("MANAGEMENT_NETWORK", "workstation-manager"),
		WSLANImage:        env("WSLAN_IMAGE", "contain-yourself-wslan:dev"),
		ApprovalsPath:     env("APP_APPROVALS_PATH", "/data/app-approvals.json"),
		AllowedImages:     make(map[string]struct{}),
	}
	if len(w.Token) < 24 {
		return Worker{}, errors.New("WORKER_TOKEN must contain at least 24 characters")
	}
	for _, image := range strings.Split(os.Getenv("WORKER_ALLOWED_IMAGES"), ",") {
		if image = strings.TrimSpace(image); image != "" {
			w.AllowedImages[image] = struct{}{}
		}
	}
	w.AllowedImages[w.WSLANImage] = struct{}{}
	return w, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

func envFloat(key string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64)
	if err != nil {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
