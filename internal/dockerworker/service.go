package dockerworker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"workstation-manager/internal/config"
	"workstation-manager/internal/vpnprofiles"
	"workstation-manager/pkg/workerapi"
)

type Service struct {
	config config.Worker
	engine *Engine
	log    *slog.Logger
}

const (
	wireGuardSecretDirectory = "/tmp"
	wireGuardSecretFilename  = "workstation-manager-wireguard.conf"
	wireGuardSecretPath      = wireGuardSecretDirectory + "/" + wireGuardSecretFilename
)

var resourceID = regexp.MustCompile(`^ws-[a-z0-9]{6,20}$`)

func NewService(cfg config.Worker, engine *Engine, logger *slog.Logger) *Service {
	return &Service{config: cfg, engine: engine, log: logger}
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("GET /v1/resources", s.auth(http.HandlerFunc(s.list)))
	mux.Handle("GET /v1/workstations/{id}", s.auth(http.HandlerFunc(s.inspect)))
	mux.Handle("GET /v1/workstations/{id}/usage", s.auth(http.HandlerFunc(s.usage)))
	mux.Handle("GET /v1/workstations/{id}/apps/{app}/logs", s.auth(http.HandlerFunc(s.logs)))
	mux.Handle("POST /v1/workstations", s.auth(http.HandlerFunc(s.provision)))
	mux.Handle("POST /v1/workstations/{id}/rebuild", s.auth(http.HandlerFunc(s.rebuild)))
	mux.Handle("POST /v1/workstations/{id}/action", s.auth(http.HandlerFunc(s.action)))
	return requestLog(s.log, mux)
}

func (s *Service) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(value) != len(s.config.Token) ||
			subtle.ConstantTimeCompare([]byte(value), []byte(s.config.Token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, workerapi.Error{Error: "invalid worker token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) health(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, workerapi.Health{Status: "unhealthy", Docker: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, workerapi.Health{Status: "ok", Docker: "reachable"})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	resources, err := s.engine.ListManaged(r.Context(), "")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resources)
}

func (s *Service) inspect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !resourceID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "invalid workstation id"})
		return
	}
	resources, err := s.engine.ListManaged(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
		return
	}
	state := "missing"
	if len(resources) > 0 {
		state = "stopped"
		for _, resource := range resources {
			if resource.State == "running" {
				state = "running"
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, workerapi.WorkstationStatus{
		WorkstationID: id, State: state, Resources: resources,
	})
}

func (s *Service) usage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !resourceID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "invalid workstation id"})
		return
	}
	resources, err := s.engine.ListManaged(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
		return
	}
	result := workerapi.UsageResponse{WorkstationID: id}
	for _, resource := range resources {
		item := workerapi.ResourceUsage{
			Name: resource.Name, Kind: resource.Kind, AppID: resource.AppID, State: resource.State,
		}
		if resource.State == "running" {
			measured, err := s.engine.ContainerStats(r.Context(), resource)
			if err != nil {
				item.Error = err.Error()
			} else {
				item = measured
			}
		}
		result.Resources = append(result.Resources, item)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) logs(w http.ResponseWriter, r *http.Request) {
	id, appID := r.PathValue("id"), r.PathValue("app")
	if !resourceID.MatchString(id) || !regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`).MatchString(appID) {
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "invalid workstation or app id"})
		return
	}
	tail := 200
	if raw := r.URL.Query().Get("tail"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "tail must be between 1 and 1000"})
			return
		}
		tail = parsed
	}
	resources, err := s.engine.ListManaged(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
		return
	}
	containerName := ""
	for _, resource := range resources {
		if resource.Kind == "app" && resource.AppID == appID {
			containerName = resource.Name
			break
		}
	}
	if containerName == "" {
		writeJSON(w, http.StatusNotFound, workerapi.Error{Error: "app container not found"})
		return
	}
	output, err := s.engine.ContainerLogs(r.Context(), containerName, tail)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, workerapi.LogResponse{
		WorkstationID: id, AppID: appID, Lines: tail, Logs: output,
	})
}

func (s *Service) provision(w http.ResponseWriter, r *http.Request) {
	var request workerapi.ProvisionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if err := s.validateProvision(request); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, workerapi.Error{Error: err.Error()})
		return
	}
	if err := s.provisionResources(r.Context(), request); err != nil {
		s.log.Error("provision failed", "workstation_id", request.WorkstationID, "error", err)
		writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
		return
	}
	resources, _ := s.engine.ListManaged(r.Context(), request.WorkstationID)
	writeJSON(w, http.StatusCreated, workerapi.WorkstationStatus{
		WorkstationID: request.WorkstationID, State: "running", Resources: resources,
	})
}

func (s *Service) rebuild(w http.ResponseWriter, r *http.Request) {
	var request workerapi.ProvisionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if request.WorkstationID != r.PathValue("id") {
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "path and request workstation ids differ"})
		return
	}
	if err := s.validateProvision(request); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, workerapi.Error{Error: err.Error()})
		return
	}
	if err := s.rebuildResources(r.Context(), request); err != nil {
		s.log.Error("rebuild failed", "workstation_id", request.WorkstationID, "error", err)
		writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
		return
	}
	resources, _ := s.engine.ListManaged(r.Context(), request.WorkstationID)
	writeJSON(w, http.StatusOK, workerapi.WorkstationStatus{
		WorkstationID: request.WorkstationID, State: "running", Resources: resources,
	})
}

func (s *Service) validateProvision(request workerapi.ProvisionRequest) error {
	if !resourceID.MatchString(request.WorkstationID) {
		return errors.New("invalid workstation id")
	}
	if request.CPU <= 0 || request.CPU > 32 || request.MemoryMB < 128 ||
		request.MemoryMB > 131072 || request.PIDLimit < 32 || request.PIDLimit > 32768 {
		return errors.New("workstation resource limits are outside the allowed range")
	}
	if request.VPNRequired {
		if request.VPNProfile == nil {
			return errors.New("VPN is required but no VPN profile was selected")
		}
		if _, err := vpnprofiles.Parse(request.VPNProfile.WireGuardConfig); err != nil {
			return fmt.Errorf("invalid WireGuard profile: %w", err)
		}
	} else if request.VPNProfile != nil {
		return errors.New("non-VPN workstation cannot select a VPN profile")
	}
	ids, ports := make(map[string]bool), make(map[int]bool)
	for _, app := range request.Apps {
		if !regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`).MatchString(app.ID) {
			return fmt.Errorf("invalid app id %q", app.ID)
		}
		if ids[app.ID] || ports[app.InternalPort] {
			return errors.New("app ids and internal ports must be unique")
		}
		ids[app.ID], ports[app.InternalPort] = true, true
		if _, allowed := s.config.AllowedImages[app.Image]; !allowed {
			return fmt.Errorf("image %q is not approved", app.Image)
		}
		if app.InternalPort < 1024 || app.InternalPort > 65535 {
			return fmt.Errorf("app %s has an invalid internal port", app.ID)
		}
		if app.CPU <= 0 || app.CPU > request.CPU || app.MemoryMB < 64 || app.MemoryMB > request.MemoryMB {
			return fmt.Errorf("app %s resource limits are invalid", app.ID)
		}
		if app.ShmSizeMB < 0 || app.ShmSizeMB > 2048 || app.ShmSizeMB > app.MemoryMB {
			return fmt.Errorf("app %s shared memory limit is invalid", app.ID)
		}
		allowedEnvironment := map[string]bool{
			"PUID": true, "PGID": true, "TZ": true, "HARDEN_DESKTOP": true,
			"DISABLE_OPEN_TOOLS": true, "DISABLE_SUDO": true,
			"DISABLE_TERMINALS": true, "CHROME_CLI": true,
		}
		for key, value := range app.Environment {
			if !allowedEnvironment[key] || len(value) > 512 ||
				strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("app %s environment is invalid", app.ID)
			}
		}
		for _, capability := range app.Capabilities {
			if capability != "NET_RAW" {
				return fmt.Errorf("app capability %q is not approved", capability)
			}
		}
		if app.HealthPath != "" && (!strings.HasPrefix(app.HealthPath, "/") || strings.Contains(app.HealthPath, "..")) {
			return fmt.Errorf("app %s health path is invalid", app.ID)
		}
		for _, storage := range app.Storage {
			if storage.Type != "workspace" && storage.Type != "app-data" &&
				storage.Type != "shell-home" && storage.Type != "temporary" {
				return fmt.Errorf("storage type %q is not approved", storage.Type)
			}
			if !filepath.IsAbs(storage.Target) || strings.Contains(filepath.Clean(storage.Target), "..") {
				return fmt.Errorf("storage target %q is invalid", storage.Target)
			}
			if storage.OwnerUID < 0 || storage.OwnerUID > 65535 ||
				storage.OwnerGID < 0 || storage.OwnerGID > 65535 ||
				(storage.OwnerUID == 0) != (storage.OwnerGID == 0) {
				return fmt.Errorf("storage owner for %q is invalid", storage.Target)
			}
		}
	}
	return nil
}

func (s *Service) provisionResources(ctx context.Context, request workerapi.ProvisionRequest) error {
	if err := s.prepareImages(ctx, request); err != nil {
		return err
	}
	return s.createResources(ctx, request)
}

func (s *Service) prepareImages(ctx context.Context, request workerapi.ProvisionRequest) error {
	for _, app := range request.Apps {
		if err := s.engine.Pull(ctx, app.Image); err != nil {
			return fmt.Errorf("pull app %s: %w", app.ID, err)
		}
	}
	if request.VPNRequired {
		if err := s.engine.Pull(ctx, s.config.VPNImage); err != nil {
			return fmt.Errorf("pull VPN gateway: %w", err)
		}
	}
	return nil
}

func (s *Service) rebuildResources(ctx context.Context, request workerapi.ProvisionRequest) error {
	if err := s.prepareImages(ctx, request); err != nil {
		return err
	}
	resources, err := s.engine.ListManaged(ctx, request.WorkstationID)
	if err != nil {
		return err
	}
	sort.SliceStable(resources, func(i, j int) bool {
		return resourceRank(resources[i].Kind) > resourceRank(resources[j].Kind)
	})
	for _, resource := range resources {
		if resource.Kind != "app" && resource.Kind != "vpn" {
			continue
		}
		if err := s.engine.ContainerAction(ctx, resource.Name, "delete"); err != nil {
			return fmt.Errorf("replace container %s: %w", resource.Name, err)
		}
	}
	return s.createResources(ctx, request)
}

func (s *Service) createResources(ctx context.Context, request workerapi.ProvisionRequest) error {
	labels := baseLabels(request.WorkstationID, "volume")
	workspaceVolume := name(request.WorkstationID, "workspace")
	if err := s.engine.CreateVolume(ctx, workspaceVolume, labels); err != nil {
		return fmt.Errorf("create workspace volume: %w", err)
	}
	networkMode := s.config.ManagementNetwork
	if request.VPNRequired {
		gatewayName := name(request.WorkstationID, "vpn")
		ports := make([]int, 0, len(request.Apps))
		for _, app := range request.Apps {
			ports = append(ports, app.InternalPort)
		}
		sort.Ints(ports)
		portStrings := make([]string, 0, len(ports))
		for _, port := range ports {
			portStrings = append(portStrings, strconv.Itoa(port))
		}
		vpnEnvironment := map[string]string{
			"VPN_SERVICE_PROVIDER":      "custom",
			"VPN_TYPE":                  "wireguard",
			"WIREGUARD_CONF_SECRETFILE": wireGuardSecretPath,
			"FIREWALL_INPUT_PORTS":      strings.Join(portStrings, ","),
		}
		if err := s.engine.CreateContainer(ctx, ContainerConfig{
			Name: gatewayName, Image: s.config.VPNImage, Environment: vpnEnvironment,
			Labels: baseLabels(request.WorkstationID, "vpn"), NetworkMode: s.config.ManagementNetwork,
			MemoryBytes: 512 * 1024 * 1024, NanoCPUs: 500_000_000, PIDLimit: 256,
			CapAdd: []string{"NET_ADMIN"},
			Devices: []map[string]string{{
				"PathOnHost": "/dev/net/tun", "PathInContainer": "/dev/net/tun", "CgroupPermissions": "rwm",
			}},
			Ports: ports,
		}); err != nil {
			return fmt.Errorf("create VPN gateway: %w", err)
		}
		if err := s.engine.CopyFile(ctx, gatewayName, wireGuardSecretDirectory,
			wireGuardSecretFilename, []byte(request.VPNProfile.WireGuardConfig), 0o600); err != nil {
			return fmt.Errorf("inject WireGuard profile: %w", err)
		}
		if err := s.engine.ContainerAction(ctx, gatewayName, "start"); err != nil {
			return fmt.Errorf("start VPN gateway: %w", err)
		}
		if err := s.waitContainerHealthy(ctx, gatewayName, 90*time.Second); err != nil {
			return fmt.Errorf("VPN gateway did not become healthy: %w", err)
		}
		networkMode = "container:" + gatewayName
	}
	for _, app := range request.Apps {
		appLabels := baseLabels(request.WorkstationID, "app")
		appLabels[workerapi.LabelAppID] = app.ID
		mounts := make([]Mount, 0, len(app.Storage))
		for _, storage := range app.Storage {
			source := workspaceVolume
			if storage.Type != "workspace" {
				source = name(request.WorkstationID, app.ID+"-"+storage.Type)
				if err := s.engine.CreateVolume(ctx, source, baseLabels(request.WorkstationID, "volume")); err != nil {
					return fmt.Errorf("create app volume: %w", err)
				}
			}
			mounts = append(mounts, Mount{Source: source, Target: storage.Target})
		}
		if err := s.initializeStorage(ctx, request.WorkstationID, app, mounts); err != nil {
			return fmt.Errorf("initialize storage for app %s: %w", app.ID, err)
		}
		if err := s.engine.CreateContainer(ctx, ContainerConfig{
			Name: name(request.WorkstationID, "app-"+app.ID), Image: app.Image,
			Command: app.Command, Environment: app.Environment,
			Labels: appLabels, NetworkMode: networkMode,
			MemoryBytes: int64(app.MemoryMB) * 1024 * 1024,
			NanoCPUs:    int64(app.CPU * 1_000_000_000), PIDLimit: request.PIDLimit,
			ShmSizeBytes: int64(app.ShmSizeMB) * 1024 * 1024,
			CapAdd:       app.Capabilities, Mounts: mounts, Ports: []int{app.InternalPort},
		}); err != nil {
			return fmt.Errorf("create app %s: %w", app.ID, err)
		}
		if err := s.engine.ContainerAction(ctx, name(request.WorkstationID, "app-"+app.ID), "start"); err != nil {
			return fmt.Errorf("start app %s: %w", app.ID, err)
		}
		host := name(request.WorkstationID, "app-"+app.ID)
		if request.VPNRequired {
			host = name(request.WorkstationID, "vpn")
		}
		if app.HealthPath != "" {
			requestTimeout := time.Duration(app.HealthTimeoutSeconds) * time.Second
			if requestTimeout <= 0 {
				requestTimeout = 5 * time.Second
			}
			if err := waitHTTP(ctx, host, app.InternalPort, app.HealthPath, requestTimeout, 90*time.Second); err != nil {
				return fmt.Errorf("app %s did not become healthy: %w", app.ID, err)
			}
		}
	}
	return nil
}

func (s *Service) initializeStorage(ctx context.Context, workstationID string,
	app workerapi.AppSpec, mounts []Mount) error {
	var targets []string
	var owner string
	for index, storage := range app.Storage {
		if storage.OwnerUID == 0 && storage.OwnerGID == 0 {
			continue
		}
		candidate := strconv.Itoa(storage.OwnerUID) + ":" + strconv.Itoa(storage.OwnerGID)
		if owner != "" && owner != candidate {
			return errors.New("one app cannot request multiple storage owners")
		}
		owner = candidate
		targets = append(targets, mounts[index].Target)
	}
	if len(targets) == 0 {
		return nil
	}
	initName := name(workstationID, "init-"+app.ID)
	command := append([]string{"-R", owner}, targets...)
	if err := s.engine.CreateContainer(ctx, ContainerConfig{
		Name: initName, Image: app.Image, Entrypoint: []string{"chown"}, Command: command,
		User: "0:0", Labels: baseLabels(workstationID, "init"),
		NetworkMode: "none", MemoryBytes: 128 * 1024 * 1024,
		NanoCPUs: 100_000_000, PIDLimit: 64, Mounts: mounts,
	}); err != nil {
		return err
	}
	if err := s.engine.ContainerAction(ctx, initName, "start"); err != nil {
		return err
	}
	if err := s.engine.WaitContainer(ctx, initName); err != nil {
		return err
	}
	return s.engine.ContainerAction(ctx, initName, "delete")
}

func (s *Service) waitContainerHealthy(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var last string
	for {
		running, healthy, health, err := s.engine.ContainerState(ctx, name)
		if err == nil {
			last = health
			if running && healthy {
				return nil
			}
			if !running {
				return errors.New("container exited")
			}
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out (last state: %s)", last)
		case <-ticker.C:
		}
	}
}

func waitHTTP(ctx context.Context, host string, port int, path string, requestTimeout, totalTimeout time.Duration) error {
	deadline := time.NewTimer(totalTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: requestTimeout}
	endpoint := fmt.Sprintf("http://%s:%d%s", host, port, path)
	var last string
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode < 500 {
				return nil
			}
			last = response.Status
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out checking %s (last result: %s)", endpoint, last)
		case <-ticker.C:
		}
	}
}

func (s *Service) action(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !resourceID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "invalid workstation id"})
		return
	}
	var request workerapi.ActionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	switch request.Action {
	case "start", "stop", "restart":
		resources, err := s.engine.ListManaged(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
			return
		}
		if request.Action == "start" {
			sort.SliceStable(resources, func(i, j int) bool {
				return resourceRank(resources[i].Kind) < resourceRank(resources[j].Kind)
			})
		} else if request.Action == "stop" {
			sort.SliceStable(resources, func(i, j int) bool {
				return resourceRank(resources[i].Kind) > resourceRank(resources[j].Kind)
			})
		}
		for _, resource := range resources {
			if err := s.engine.ContainerAction(r.Context(), resource.Name, request.Action); err != nil {
				writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
				return
			}
			if request.Action == "start" && resource.Kind == "vpn" {
				if err := s.waitContainerHealthy(r.Context(), resource.Name, 90*time.Second); err != nil {
					writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
					return
				}
			}
		}
	case "delete":
		if err := s.engine.RemoveWorkstation(r.Context(), id); err != nil {
			writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "action must be start, stop, restart, or delete"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func resourceRank(kind string) int {
	if kind == "vpn" {
		return 0
	}
	if kind == "app" {
		return 1
	}
	return 2
}

func baseLabels(workstationID, resourceType string) map[string]string {
	return map[string]string{
		workerapi.LabelManagedBy:     workerapi.ManagedByValue,
		workerapi.LabelWorkstationID: workstationID,
		workerapi.LabelResourceType:  resourceType,
	}
}

func name(workstationID, suffix string) string {
	return "wm-" + workstationID + "-" + suffix
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "invalid JSON: " + err.Error()})
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "request body must contain one JSON value"})
		return errors.New("trailing JSON")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("worker request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
