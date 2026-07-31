// Command wslan is the authenticated ingress for a workstation-local network.
// It only proxies to app IDs and ports supplied by the trusted worker.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	tokenHeader = "X-Contain-WSLAN-Token"
	appHeader   = "X-Contain-WSLAN-App"
)

type gateway struct {
	token  string
	mode   string
	apps   map[string]int
	client *http.Transport
	log    *slog.Logger
}

func main() {
	token := os.Getenv("WSLAN_TOKEN")
	if len(token) < 24 {
		fatal(errors.New("WSLAN_TOKEN must contain at least 24 characters"))
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("WSLAN_MODE")))
	if mode != "direct" && mode != "wireguard" {
		fatal(fmt.Errorf("unsupported WSLAN_MODE %q", mode))
	}
	apps, err := parseApps(os.Getenv("WSLAN_APPS"))
	if err != nil {
		fatal(err)
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "udp", "127.0.0.11:53")
		},
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Resolver: resolver}
	g := &gateway{
		token: token, mode: mode, apps: apps, log: slog.Default(),
		client: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     false,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", g.health)
	mux.HandleFunc("/", g.proxy)
	server := &http.Server{
		Addr:              env("WSLAN_LISTEN", "0.0.0.0:9000"),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	g.log.Info("WSLAN ingress ready", "mode", mode, "apps", len(apps), "listen", server.Addr)
	fatal(server.ListenAndServe())
}

func parseApps(value string) (map[string]int, error) {
	result := make(map[string]int)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		id, rawPort, ok := strings.Cut(item, "=")
		port, err := strconv.Atoi(rawPort)
		if !ok || !validID(id) || err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid WSLAN app mapping %q", item)
		}
		result[id] = port
	}
	return result, nil
}

func validID(value string) bool {
	if len(value) < 2 || len(value) > 32 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func (g *gateway) health(w http.ResponseWriter, _ *http.Request) {
	if g.mode == "wireguard" {
		if err := exec.Command("wg", "show", "wg0").Run(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unhealthy", "mode": g.mode, "error": "WireGuard interface is unavailable",
			})
			return
		}
		if os.Getenv("WSLAN_SKIP_HANDSHAKE_CHECK") != "1" && !recentHandshake() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unhealthy", "mode": g.mode, "error": "WireGuard has no recent handshake",
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "mode": g.mode})
}

func recentHandshake() bool {
	internalIP := net.ParseIP(os.Getenv("WSLAN_INTERNAL_ADDRESS"))
	if internalIP == nil {
		return false
	}
	connection, err := net.DialUDP("udp",
		&net.UDPAddr{IP: internalIP},
		&net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 53})
	if err == nil {
		_, _ = connection.Write([]byte{0})
		connection.Close()
	}
	output, err := exec.Command("wg", "show", "wg0", "latest-handshakes").Output()
	if err != nil {
		return false
	}
	return hasRecentHandshake(string(output), time.Now().Unix())
}

func hasRecentHandshake(output string, now int64) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		latest, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr == nil && latest > 0 && now-latest <= 180 {
			return true
		}
	}
	return false
}

func (g *gateway) proxy(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get(tokenHeader)
	if len(provided) != len(g.token) ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(g.token)) != 1 {
		http.Error(w, "invalid WSLAN token", http.StatusUnauthorized)
		return
	}
	appID := r.Header.Get(appHeader)
	port, ok := g.apps[appID]
	if !ok {
		http.Error(w, "unknown WSLAN app", http.StatusNotFound)
		return
	}
	target, _ := url.Parse(fmt.Sprintf("http://app-%s:%d", appID, port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = g.client
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		original(request)
		request.Header.Del(tokenHeader)
		request.Header.Del(appHeader)
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, err error) {
		g.log.Warn("WSLAN upstream unavailable", "app_id", appID, "error", err)
		http.Error(rw, "application is unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fatal(err error) {
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("WSLAN stopped", "error", err)
		os.Exit(1)
	}
}
