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

	"workstation-manager/pkg/workerapi"
)

const (
	tokenHeader = "X-Contain-WSLAN-Token"
	appHeader   = "X-Contain-WSLAN-App"
	// handshakeMaxAge matches WireGuard's own idea of a live session: peers
	// rehandshake well inside this, so anything older means the tunnel is dead.
	handshakeMaxAge = 180
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
	if !supportedMode(mode) {
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
	mux.HandleFunc("GET /status", g.status)
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

// supportedMode mirrors internal/egress. The gateway is a separate image that
// can be a different build than the worker, so it validates the mode itself
// rather than trusting whatever it was handed.
func supportedMode(value string) bool {
	switch value {
	case "direct", "wireguard", "host-gateway", "ipv6":
		return true
	}
	return false
}

// healthChecksTunnel reports whether the mode has a tunnel whose liveness
// decides gateway health. Only WireGuard fails closed.
func healthChecksTunnel(mode string) bool { return mode == "wireguard" }

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
	if healthChecksTunnel(g.mode) {
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

// status reports how traffic is actually leaving this workstation. It is
// authenticated with the same token as the proxy: the answer names the VPN exit,
// which is not something to hand out unauthenticated.
func (g *gateway) status(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r) {
		http.Error(w, "invalid WSLAN token", http.StatusUnauthorized)
		return
	}
	result := workerapi.EgressStatus{
		Mode: g.mode,
		// Only the tunnelled mode stops traffic when its path dies; the direct
		// modes have nothing to fail closed to.
		FailsClosed: g.mode == "wireguard",
	}
	if g.mode != "wireguard" {
		result.Healthy = true
		writeJSON(w, http.StatusOK, result)
		return
	}
	output, err := exec.Command("wg", "show", "wg0", "dump").Output()
	if err != nil {
		result.Error = "WireGuard interface is unavailable"
		result.Tunnel = &workerapi.TunnelStatus{HandshakeAgeSeconds: -1}
		writeJSON(w, http.StatusOK, result)
		return
	}
	tunnel := parseWireGuardDump(string(output), time.Now().Unix())
	result.Tunnel = &tunnel
	result.Healthy = tunnel.Up
	writeJSON(w, http.StatusOK, result)
}

func (g *gateway) authorized(r *http.Request) bool {
	provided := r.Header.Get(tokenHeader)
	return len(provided) == len(g.token) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(g.token)) == 1
}

// parseWireGuardDump reads `wg show <iface> dump`.
//
// The first line of that output describes the interface and its second field is
// the PRIVATE KEY. It is skipped without being examined, and only peer lines are
// read, so no key material can reach a response. Peer fields are:
//
//	public-key preshared-key endpoint allowed-ips latest-handshake rx tx keepalive
func parseWireGuardDump(output string, now int64) workerapi.TunnelStatus {
	tunnel := workerapi.TunnelStatus{HandshakeAgeSeconds: -1}
	for index, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if index == 0 {
			// Interface line: contains the private key. Never parsed.
			continue
		}
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 7 {
			continue
		}
		if endpoint := fields[2]; endpoint != "" && endpoint != "(none)" {
			tunnel.Endpoint = endpoint
		}
		if handshake, err := strconv.ParseInt(fields[4], 10, 64); err == nil && handshake > 0 {
			age := now - handshake
			if age < 0 {
				age = 0
			}
			// Keep the freshest peer, which is the one carrying traffic.
			if tunnel.HandshakeAgeSeconds < 0 || age < tunnel.HandshakeAgeSeconds {
				tunnel.HandshakeAgeSeconds = age
			}
		}
		if received, err := strconv.ParseUint(fields[5], 10, 64); err == nil {
			tunnel.ReceivedBytes += received
		}
		if sent, err := strconv.ParseUint(fields[6], 10, 64); err == nil {
			tunnel.SentBytes += sent
		}
	}
	// The interface exists, so the tunnel is up once a handshake has completed
	// recently enough that the peer still considers the session live.
	tunnel.Up = tunnel.HandshakeAgeSeconds >= 0 && tunnel.HandshakeAgeSeconds <= handshakeMaxAge
	return tunnel
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
		if parseErr == nil && latest > 0 && now-latest <= handshakeMaxAge {
			return true
		}
	}
	return false
}

func (g *gateway) proxy(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r) {
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
