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
	"sync"
	"time"

	"workstation-manager/internal/config"
	"workstation-manager/internal/egress"
	"workstation-manager/internal/vpnprofiles"
	"workstation-manager/pkg/workerapi"
)

type Service struct {
	config    config.Worker
	engine    *Engine
	log       *slog.Logger
	approvals *approvalStore

	captureMu  sync.Mutex
	captureCtx context.Context
	captures   map[string]struct{}
}

const (
	wireGuardSecretDirectory = "/run/wslan"
	wireGuardSecretFilename  = "wg0.conf"
	wireGuardSecretPath      = wireGuardSecretDirectory + "/" + wireGuardSecretFilename
	wslanIngressPort         = 9000
)

var resourceID = regexp.MustCompile(`^ws-[a-z0-9]{6,20}$`)

func NewService(cfg config.Worker, engine *Engine, logger *slog.Logger) (*Service, error) {
	approvals, err := newApprovalStore(cfg.ApprovalsPath)
	if err != nil {
		return nil, fmt.Errorf("open app approvals: %w", err)
	}
	return &Service{
		config: cfg, engine: engine, log: logger, approvals: approvals,
		captures: make(map[string]struct{}),
	}, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("POST /v1/apps/approve", s.auth(http.HandlerFunc(s.approveApp)))
	mux.Handle("GET /v1/resources", s.auth(http.HandlerFunc(s.list)))
	mux.Handle("GET /v1/workstations/{id}", s.auth(http.HandlerFunc(s.inspect)))
	mux.Handle("GET /v1/workstations/{id}/usage", s.auth(http.HandlerFunc(s.usage)))
	mux.Handle("GET /v1/workstations/{id}/egress", s.auth(http.HandlerFunc(s.egress)))
	mux.Handle("GET /v1/workstations/{id}/apps/{app}/logs", s.auth(http.HandlerFunc(s.logs)))
	mux.Handle("POST /v1/workstations", s.auth(http.HandlerFunc(s.provision)))
	mux.Handle("POST /v1/workstations/{id}/rebuild", s.auth(http.HandlerFunc(s.rebuild)))
	mux.Handle("POST /v1/workstations/{id}/action", s.auth(http.HandlerFunc(s.action)))
	return requestLog(s.log, mux)
}

func (s *Service) approveApp(w http.ResponseWriter, r *http.Request) {
	var request workerapi.AppApproval
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if err := s.validateApp(request.App, 32, 131072); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, workerapi.Error{Error: err.Error()})
		return
	}
	if request.App.Version == "" || request.App.ManifestSHA256 == "" ||
		!regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`).MatchString(request.App.Version) ||
		!regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(request.App.ManifestSHA256) {
		writeJSON(w, http.StatusUnprocessableEntity,
			workerapi.Error{Error: "version and manifest SHA-256 are required"})
		return
	}
	status, err := s.approvals.approve(request.App)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, workerapi.Error{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, status)
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

// egress asks a workstation's own gateway how its traffic is actually leaving.
// The gateway reads this from local kernel state, so answering does not send
// anything outside the workstation.
func (s *Service) egress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !resourceID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, workerapi.Error{Error: "invalid workstation id"})
		return
	}
	result := workerapi.EgressStatus{WorkstationID: id}
	gateway := name(id, "wslan")
	running, _, _, err := s.engine.ContainerState(r.Context(), gateway)
	if err != nil || !running {
		result.Error = "workstation gateway is not running"
		writeJSON(w, http.StatusOK, result)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s:%d/status", gateway, wslanIngressPort), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, workerapi.Error{Error: err.Error()})
		return
	}
	request.Header.Set("X-Contain-WSLAN-Token", s.config.Token)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		result.Error = "gateway did not answer: " + err.Error()
		writeJSON(w, http.StatusOK, result)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		result.Error = "gateway returned " + response.Status
		writeJSON(w, http.StatusOK, result)
		return
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&result); err != nil {
		result.Error = "gateway sent an unreadable status"
		writeJSON(w, http.StatusOK, result)
		return
	}
	result.WorkstationID = id
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
	if output, ok := s.persistedLogs(id, appID, tail); ok {
		writeJSON(w, http.StatusOK, workerapi.LogResponse{
			WorkstationID: id, AppID: appID, Lines: tail, Logs: output,
		})
		return
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
	mode := egress.Resolve(request.EgressMode, request.VPNRequired)
	if request.EgressMode != "" {
		if _, err := egress.Parse(request.EgressMode); err != nil {
			return err
		}
	}
	if mode.RequiresVPNProfile() {
		if request.VPNProfile == nil {
			return errors.New("VPN is required but no VPN profile was selected")
		}
		if _, err := vpnprofiles.Parse(request.VPNProfile.WireGuardConfig); err != nil {
			return fmt.Errorf("invalid WireGuard profile: %w", err)
		}
	} else if request.VPNProfile != nil {
		return errors.New("only a wireguard-egress workstation can carry a VPN profile")
	}
	if request.WorkspaceImage != "" {
		if !pinnedImageReference(request.WorkspaceImage) {
			return errors.New("workspace image is not pinned")
		}
		if _, allowed := s.config.AllowedImages[request.WorkspaceImage]; !allowed {
			return fmt.Errorf("workspace image %q is not approved", request.WorkspaceImage)
		}
	}
	ids := make(map[string]bool)
	for _, app := range request.Apps {
		if ids[app.ID] {
			return errors.New("app ids must be unique")
		}
		ids[app.ID] = true
		if _, allowed := s.config.AllowedImages[app.Image]; !allowed && !s.approvals.allowed(app) {
			return fmt.Errorf("image %q is not approved", app.Image)
		}
		if err := s.validateApp(app, request.CPU, request.MemoryMB); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) validateApp(app workerapi.AppSpec, maxCPU float64, maxMemoryMB int) error {
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`).MatchString(app.ID) {
		return fmt.Errorf("invalid app id %q", app.ID)
	}
	if !pinnedImageReference(app.Image) {
		return fmt.Errorf("app %s image is not pinned", app.ID)
	}
	if app.InternalPort < 1024 || app.InternalPort > 65535 {
		return fmt.Errorf("app %s has an invalid internal port", app.ID)
	}
	if app.CPU <= 0 || app.CPU > maxCPU || app.MemoryMB < 64 || app.MemoryMB > maxMemoryMB {
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
	return nil
}

func pinnedImageReference(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		return false
	}
	if marker := strings.LastIndex(value, "@sha256:"); marker >= 0 {
		hash := value[marker+len("@sha256:"):]
		return marker > 0 && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash)
	}
	lastSlash := strings.LastIndex(value, "/")
	lastColon := strings.LastIndex(value, ":")
	return lastColon > lastSlash && lastColon < len(value)-1 &&
		value[lastColon+1:] != "latest"
}

func (s *Service) provisionResources(ctx context.Context, request workerapi.ProvisionRequest) error {
	if err := s.prepareImages(ctx, request); err != nil {
		return err
	}
	return s.createResources(ctx, request)
}

func (s *Service) prepareImages(ctx context.Context, request workerapi.ProvisionRequest) error {
	if err := s.engine.EnsureImage(ctx, s.config.WSLANImage); err != nil {
		return fmt.Errorf("pull WSLAN system image: %w", err)
	}
	if request.WorkspaceImage != "" {
		if err := s.engine.EnsureImage(ctx, request.WorkspaceImage); err != nil {
			return fmt.Errorf("pull workspace image: %w", err)
		}
	}
	for _, app := range request.Apps {
		if err := s.engine.Pull(ctx, app.Image); err != nil {
			return fmt.Errorf("pull app %s: %w", app.ID, err)
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
		if resource.Kind != "app" && resource.Kind != "vpn" &&
			resource.Kind != "wslan" && resource.Kind != "sandbox" {
			continue
		}
		if err := s.engine.ContainerAction(ctx, resource.Name, "delete"); err != nil {
			return fmt.Errorf("replace container %s: %w", resource.Name, err)
		}
	}
	networks, err := s.engine.listManagedNetworks(ctx, request.WorkstationID)
	if err != nil {
		return err
	}
	for _, network := range networks {
		if err := s.engine.DeleteNetwork(ctx, network); err != nil {
			return fmt.Errorf("replace network %s: %w", network, err)
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
	if err := s.seedWorkspace(ctx, request, workspaceVolume); err != nil {
		return fmt.Errorf("seed workspace: %w", err)
	}
	mode := egress.Resolve(request.EgressMode, request.VPNRequired)
	wslanNetwork := name(request.WorkstationID, "wslan")
	if err := s.engine.CreateNetwork(ctx, wslanNetwork,
		baseLabels(request.WorkstationID, "network"), mode.RequiresIPv6()); err != nil {
		return fmt.Errorf("create WSLAN network: %w", err)
	}
	networkInfo, err := s.engine.InspectNetwork(ctx, wslanNetwork)
	if err != nil {
		return fmt.Errorf("inspect WSLAN network: %w", err)
	}
	appMappings := make([]string, 0, len(request.Apps))
	for _, app := range request.Apps {
		appMappings = append(appMappings, app.ID+"="+strconv.Itoa(app.InternalPort))
	}
	sort.Strings(appMappings)
	gatewayName := name(request.WorkstationID, "wslan")
	gatewayConfig := ContainerConfig{
		Name: gatewayName, Image: s.config.WSLANImage,
		Environment: map[string]string{
			"WSLAN_ROLE": "gateway", "WSLAN_MODE": string(mode),
			"WSLAN_INTERNAL_CIDR": networkInfo.Subnet,
			"WSLAN_TOKEN":         s.config.Token,
			"WSLAN_APPS":          strings.Join(appMappings, ","),
		},
		Labels: baseLabels(request.WorkstationID, "wslan"), NetworkMode: s.config.ManagementNetwork,
		MemoryBytes: 256 * 1024 * 1024, NanoCPUs: 500_000_000, PIDLimit: 256,
		CapAdd: []string{"NET_ADMIN"}, Ports: []int{wslanIngressPort},
		Sysctls: map[string]string{"net.ipv4.ip_forward": "1"},
	}
	if mode.RequiresIPv6() {
		gatewayConfig.Sysctls["net.ipv6.conf.all.forwarding"] = "1"
		gatewayConfig.Sysctls["net.ipv6.conf.all.disable_ipv6"] = "0"
	}
	if mode.RequiresVPNProfile() {
		gatewayConfig.Devices = []map[string]string{{
			"PathOnHost": "/dev/net/tun", "PathInContainer": "/dev/net/tun", "CgroupPermissions": "rwm",
		}}
	}
	if err := s.engine.CreateContainer(ctx, gatewayConfig); err != nil {
		return fmt.Errorf("create WSLAN gateway: %w", err)
	}
	if err := s.engine.ConnectNetwork(ctx, wslanNetwork, gatewayName, []string{"wslan-gateway"}); err != nil {
		return fmt.Errorf("connect WSLAN gateway: %w", err)
	}
	if mode.RequiresVPNProfile() {
		if err := s.engine.CopyFile(ctx, gatewayName, wireGuardSecretDirectory,
			wireGuardSecretFilename, []byte(request.VPNProfile.WireGuardConfig), 0o600); err != nil {
			return fmt.Errorf("inject WireGuard profile: %w", err)
		}
	}
	if err := s.engine.ContainerAction(ctx, gatewayName, "start"); err != nil {
		return fmt.Errorf("start WSLAN gateway: %w", err)
	}
	s.captureContainer(request.WorkstationID, "wslan", gatewayName)
	if err := waitHTTP(ctx, gatewayName, wslanIngressPort, "/healthz",
		5*time.Second, 90*time.Second, nil); err != nil {
		return fmt.Errorf("WSLAN gateway did not become healthy: %w", err)
	}
	networkInfo, err = s.engine.InspectNetwork(ctx, wslanNetwork)
	if err != nil {
		return fmt.Errorf("inspect connected WSLAN: %w", err)
	}
	gatewayAddress := networkInfo.Containers[gatewayName]
	if gatewayAddress == "" {
		return errors.New("WSLAN gateway has no internal address")
	}
	for _, app := range request.Apps {
		sandboxName := name(request.WorkstationID, "net-"+app.ID)
		sandboxLabels := baseLabels(request.WorkstationID, "sandbox")
		sandboxLabels[workerapi.LabelAppID] = app.ID
		if err := s.engine.CreateContainer(ctx, ContainerConfig{
			Name: sandboxName, Image: s.config.WSLANImage,
			Environment: map[string]string{
				"WSLAN_ROLE": "sandbox", "WSLAN_GATEWAY": gatewayAddress,
			},
			Labels: sandboxLabels, NetworkMode: wslanNetwork,
			MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
			CapAdd: []string{"NET_ADMIN"}, DNS: []string{gatewayAddress},
			NetworkAliases: []string{"app-" + app.ID, app.ID},
		}); err != nil {
			return fmt.Errorf("create network sandbox for app %s: %w", app.ID, err)
		}
		if err := s.engine.ContainerAction(ctx, sandboxName, "start"); err != nil {
			return fmt.Errorf("start network sandbox for app %s: %w", app.ID, err)
		}
		s.captureContainer(request.WorkstationID, "network-"+app.ID, sandboxName)
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
			Labels: appLabels, NetworkMode: "container:" + sandboxName,
			MemoryBytes: int64(app.MemoryMB) * 1024 * 1024,
			NanoCPUs:    int64(app.CPU * 1_000_000_000), PIDLimit: request.PIDLimit,
			ShmSizeBytes: int64(app.ShmSizeMB) * 1024 * 1024,
			CapAdd:       app.Capabilities, Mounts: mounts,
		}); err != nil {
			return fmt.Errorf("create app %s: %w", app.ID, err)
		}
		if err := s.engine.ContainerAction(ctx, name(request.WorkstationID, "app-"+app.ID), "start"); err != nil {
			return fmt.Errorf("start app %s: %w", app.ID, err)
		}
		s.captureContainer(request.WorkstationID, app.ID,
			name(request.WorkstationID, "app-"+app.ID))
		if app.HealthPath != "" {
			requestTimeout := time.Duration(app.HealthTimeoutSeconds) * time.Second
			if requestTimeout <= 0 {
				requestTimeout = 5 * time.Second
			}
			headers := map[string]string{
				"X-Contain-WSLAN-Token": s.config.Token,
				"X-Contain-WSLAN-App":   app.ID,
			}
			if err := waitHTTP(ctx, gatewayName, wslanIngressPort, app.HealthPath,
				requestTimeout, 90*time.Second, headers); err != nil {
				return fmt.Errorf("app %s did not become healthy: %w", app.ID, err)
			}
		}
	}
	return nil
}

// workspaceSeedDirectory is the conventional path a workspace image uses to
// publish the files it wants copied into a new workspace. Images without it
// still work: the seed run creates the marker and exits.
const workspaceSeedDirectory = "/opt/workspace-seed"

// workspaceSeedMarker records that a workspace volume has already been seeded.
// Seeding is deliberately once-per-volume so that a workstation update or
// rebuild never overwrites files the user has since changed.
const workspaceSeedMarker = "/workspace/.workstation-seeded"

// seedWorkspace populates a freshly created workspace volume from the template's
// workspace image. The seed container runs to completion and is removed; it is
// not part of the workstation's running resource set.
func (s *Service) seedWorkspace(ctx context.Context, request workerapi.ProvisionRequest,
	workspaceVolume string) error {
	if request.WorkspaceImage == "" {
		return nil
	}
	seedName := name(request.WorkstationID, "seed-workspace")
	// Delete any container left behind by an interrupted earlier attempt so the
	// run below is not silently skipped by the name-conflict reuse path.
	if err := s.engine.ContainerAction(ctx, seedName, "delete"); err != nil {
		return err
	}
	script := fmt.Sprintf(
		"set -e; if [ -e %[1]s ]; then exit 0; fi; "+
			"if [ -d %[2]s ]; then cp -a %[2]s/. /workspace/; fi; "+
			"touch %[1]s", workspaceSeedMarker, workspaceSeedDirectory)
	if err := s.engine.CreateContainer(ctx, ContainerConfig{
		Name: seedName, Image: request.WorkspaceImage,
		Entrypoint: []string{"/bin/sh"}, Command: []string{"-c", script},
		User: "0:0", Labels: baseLabels(request.WorkstationID, "seed"),
		NetworkMode: "none", MemoryBytes: 512 * 1024 * 1024,
		NanoCPUs: 1_000_000_000, PIDLimit: 128,
		Mounts: []Mount{{Source: workspaceVolume, Target: "/workspace"}},
	}); err != nil {
		return err
	}
	if err := s.engine.ContainerAction(ctx, seedName, "start"); err != nil {
		return err
	}
	if err := s.engine.WaitContainer(ctx, seedName); err != nil {
		return err
	}
	return s.engine.ContainerAction(ctx, seedName, "delete")
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

func waitHTTP(ctx context.Context, host string, port int, path string,
	requestTimeout, totalTimeout time.Duration, headers map[string]string) error {
	deadline := time.NewTimer(totalTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: requestTimeout}
	endpoint := fmt.Sprintf("http://%s:%d%s", host, port, path)
	var last string
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode < 400 {
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
		if request.Action == "restart" {
			sort.SliceStable(resources, func(i, j int) bool {
				return resourceRank(resources[i].Kind) > resourceRank(resources[j].Kind)
			})
			for _, resource := range resources {
				running, _, _, stateErr := s.engine.ContainerState(r.Context(), resource.Name)
				if stateErr != nil {
					writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: stateErr.Error()})
					return
				}
				if !running {
					continue
				}
				if err := s.engine.ContainerAction(r.Context(), resource.Name, "stop"); err != nil {
					writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
					return
				}
			}
			request.Action = "start"
		}
		sort.SliceStable(resources, func(i, j int) bool {
			if request.Action == "start" {
				return resourceRank(resources[i].Kind) < resourceRank(resources[j].Kind)
			}
			return resourceRank(resources[i].Kind) > resourceRank(resources[j].Kind)
		})
		for _, resource := range resources {
			if err := s.engine.ContainerAction(r.Context(), resource.Name, request.Action); err != nil {
				writeJSON(w, http.StatusBadGateway, workerapi.Error{Error: err.Error()})
				return
			}
			if request.Action == "start" {
				s.captureResource(resource)
			}
			if request.Action == "start" && (resource.Kind == "vpn" || resource.Kind == "wslan") {
				if resource.Kind == "wslan" {
					err = waitHTTP(r.Context(), resource.Name, wslanIngressPort, "/healthz",
						5*time.Second, 90*time.Second, nil)
				} else {
					err = s.waitContainerHealthy(r.Context(), resource.Name, 90*time.Second)
				}
				if err != nil {
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
	if kind == "vpn" || kind == "wslan" {
		return 0
	}
	if kind == "sandbox" {
		return 1
	}
	if kind == "app" {
		return 2
	}
	return 3
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
