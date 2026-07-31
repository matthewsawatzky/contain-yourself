// Package dockerworker implements a deliberately narrow Docker Engine client.
// Only the worker process imports this package.
package dockerworker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"workstation-manager/pkg/workerapi"
)

type Engine struct {
	client *http.Client
}

func NewEngine(socket string) *Engine {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	return &Engine{client: &http.Client{Transport: transport, Timeout: 10 * time.Minute}}
}

func (e *Engine) Ping(ctx context.Context) error {
	return e.do(ctx, http.MethodGet, "/_ping", nil, nil, http.StatusOK)
}

func (e *Engine) Pull(ctx context.Context, image string) error {
	path := "/images/create?fromImage=" + url.QueryEscape(image)
	response, err := e.request(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return engineError(response)
	}
	scanner := bufio.NewScanner(response.Body)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 2*1024*1024)
	for scanner.Scan() {
		var status struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &status) == nil && status.Error != "" {
			return errors.New(status.Error)
		}
	}
	return scanner.Err()
}

func (e *Engine) EnsureImage(ctx context.Context, image string) error {
	response, err := e.request(ctx, http.MethodGet,
		"/images/"+url.PathEscape(image)+"/json", nil)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusOK {
		response.Body.Close()
		return nil
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return e.Pull(ctx, image)
	}
	defer response.Body.Close()
	return engineError(response)
}

func (e *Engine) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	body := map[string]any{"Name": name, "Labels": labels}
	response, err := e.request(ctx, http.MethodPost, "/volumes/create", body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return engineError(response)
	}
	var volume struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(response.Body).Decode(&volume); err != nil {
		return fmt.Errorf("decode created volume: %w", err)
	}
	if volume.Name != name || !containsLabels(volume.Labels, labels) {
		return fmt.Errorf("refusing to reuse volume %q with mismatched ownership labels", name)
	}
	return nil
}

type ContainerConfig struct {
	Name           string
	Image          string
	Entrypoint     []string
	Command        []string
	User           string
	Environment    map[string]string
	Labels         map[string]string
	NetworkMode    string
	MemoryBytes    int64
	NanoCPUs       int64
	PIDLimit       int
	ShmSizeBytes   int64
	CapAdd         []string
	Devices        []map[string]string
	Mounts         []Mount
	Ports          []int
	DNS            []string
	Sysctls        map[string]string
	NetworkAliases []string
}

type Mount struct {
	Source string
	Target string
}

func (e *Engine) CreateContainer(ctx context.Context, config ContainerConfig) error {
	env := make([]string, 0, len(config.Environment))
	for key, value := range config.Environment {
		env = append(env, key+"="+value)
	}
	exposed := make(map[string]map[string]any)
	for _, port := range config.Ports {
		exposed[strconv.Itoa(port)+"/tcp"] = map[string]any{}
	}
	mounts := make([]map[string]string, 0, len(config.Mounts))
	for _, mount := range config.Mounts {
		mounts = append(mounts, map[string]string{
			"Type": "volume", "Source": mount.Source, "Target": mount.Target,
		})
	}
	body := map[string]any{
		"Image": config.Image, "Entrypoint": config.Entrypoint, "Cmd": config.Command,
		"User": config.User, "Env": env,
		"Labels": config.Labels, "ExposedPorts": exposed,
		"HostConfig": map[string]any{
			"NetworkMode":    config.NetworkMode,
			"Memory":         config.MemoryBytes,
			"NanoCpus":       config.NanoCPUs,
			"PidsLimit":      config.PIDLimit,
			"ShmSize":        config.ShmSizeBytes,
			"CapAdd":         config.CapAdd,
			"SecurityOpt":    []string{"no-new-privileges:true"},
			"ReadonlyRootfs": false,
			"Devices":        config.Devices,
			"Mounts":         mounts,
			"Dns":            config.DNS,
			"Sysctls":        config.Sysctls,
		},
	}
	if len(config.NetworkAliases) > 0 {
		body["NetworkingConfig"] = map[string]any{
			"EndpointsConfig": map[string]any{
				config.NetworkMode: map[string]any{"Aliases": config.NetworkAliases},
			},
		}
	}
	response, err := e.request(ctx, http.MethodPost,
		"/containers/create?name="+url.QueryEscape(config.Name), body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return e.verifyExistingContainer(ctx, config)
	}
	if response.StatusCode != http.StatusCreated {
		return engineError(response)
	}
	return nil
}

type NetworkInfo struct {
	Name       string
	Subnet     string
	Internal   bool
	Labels     map[string]string
	Containers map[string]string
}

func (e *Engine) CreateNetwork(ctx context.Context, name string, labels map[string]string) error {
	body := map[string]any{
		"Name": name, "Driver": "bridge", "Internal": true, "Attachable": false,
		"CheckDuplicate": true, "EnableIPv6": false, "Labels": labels,
	}
	response, err := e.request(ctx, http.MethodPost, "/networks/create", body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusConflict {
		return engineError(response)
	}
	info, err := e.InspectNetwork(ctx, name)
	if err != nil {
		return err
	}
	if info.Name != name || !info.Internal || !containsLabels(info.Labels, labels) {
		return fmt.Errorf("refusing to reuse network %q with mismatched settings or ownership labels", name)
	}
	return nil
}

func (e *Engine) InspectNetwork(ctx context.Context, name string) (NetworkInfo, error) {
	var inspection struct {
		Name     string            `json:"Name"`
		Internal bool              `json:"Internal"`
		Labels   map[string]string `json:"Labels"`
		IPAM     struct {
			Config []struct {
				Subnet string `json:"Subnet"`
			} `json:"Config"`
		} `json:"IPAM"`
		Containers map[string]struct {
			Name        string `json:"Name"`
			IPv4Address string `json:"IPv4Address"`
		} `json:"Containers"`
	}
	if err := e.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name),
		nil, &inspection, http.StatusOK); err != nil {
		return NetworkInfo{}, err
	}
	info := NetworkInfo{
		Name: inspection.Name, Internal: inspection.Internal, Labels: inspection.Labels,
		Containers: make(map[string]string),
	}
	if len(inspection.IPAM.Config) > 0 {
		info.Subnet = inspection.IPAM.Config[0].Subnet
	}
	for _, endpoint := range inspection.Containers {
		info.Containers[endpoint.Name] = strings.SplitN(endpoint.IPv4Address, "/", 2)[0]
	}
	if info.Subnet == "" {
		return NetworkInfo{}, fmt.Errorf("network %q has no IPv4 subnet", name)
	}
	return info, nil
}

func (e *Engine) ConnectNetwork(ctx context.Context, network, container string, aliases []string) error {
	body := map[string]any{
		"Container":      container,
		"EndpointConfig": map[string]any{"Aliases": aliases},
	}
	response, err := e.request(ctx, http.MethodPost,
		"/networks/"+url.PathEscape(network)+"/connect", body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		info, inspectErr := e.InspectNetwork(ctx, network)
		if inspectErr == nil {
			if _, exists := info.Containers[container]; exists {
				return nil
			}
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return engineError(response)
	}
	return nil
}

func (e *Engine) DeleteNetwork(ctx context.Context, name string) error {
	response, err := e.request(ctx, http.MethodDelete, "/networks/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return engineError(response)
	}
	return nil
}

func (e *Engine) WaitContainer(ctx context.Context, name string) error {
	response, err := e.request(ctx, http.MethodPost,
		"/containers/"+url.PathEscape(name)+"/wait?condition=not-running", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return engineError(response)
	}
	var result struct {
		StatusCode int `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode container wait: %w", err)
	}
	if result.Error != nil && result.Error.Message != "" {
		return errors.New(result.Error.Message)
	}
	if result.StatusCode != 0 {
		return fmt.Errorf("container exited with status %d", result.StatusCode)
	}
	return nil
}

func (e *Engine) CopyFile(ctx context.Context, container, directory, filename string,
	data []byte, mode int64) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name: filename, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	path := "/containers/" + url.PathEscape(container) + "/archive?path=" + url.QueryEscape(directory)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://docker"+path,
		bytes.NewReader(archive.Bytes()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-tar")
	response, err := e.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return engineError(response)
	}
	return nil
}

func (e *Engine) verifyExistingContainer(ctx context.Context, expected ContainerConfig) error {
	var inspection struct {
		Name   string `json:"Name"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		HostConfig struct {
			NetworkMode string `json:"NetworkMode"`
		} `json:"HostConfig"`
	}
	if err := e.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(expected.Name)+"/json",
		nil, &inspection, http.StatusOK); err != nil {
		return err
	}
	if strings.TrimPrefix(inspection.Name, "/") != expected.Name ||
		inspection.Config.Image != expected.Image ||
		inspection.HostConfig.NetworkMode != expected.NetworkMode ||
		!containsLabels(inspection.Config.Labels, expected.Labels) {
		return fmt.Errorf("refusing to reuse container %q with mismatched image, network, or ownership labels", expected.Name)
	}
	return nil
}

func containsLabels(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func (e *Engine) ContainerState(ctx context.Context, name string) (running, healthy bool, health string, err error) {
	var inspection struct {
		State struct {
			Running bool `json:"Running"`
			Health  *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	err = e.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &inspection, http.StatusOK)
	if err != nil {
		return false, false, "", err
	}
	running = inspection.State.Running
	if inspection.State.Health == nil {
		return running, running, "none", nil
	}
	health = inspection.State.Health.Status
	healthy = health == "healthy"
	return running, healthy, health, nil
}

func (e *Engine) ContainerStats(ctx context.Context, resource workerapi.Resource) (workerapi.ResourceUsage, error) {
	response, err := e.request(ctx, http.MethodGet,
		"/containers/"+url.PathEscape(resource.Name)+"/stats?stream=false&one-shot=true", nil)
	if err != nil {
		return workerapi.ResourceUsage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return workerapi.ResourceUsage{}, engineError(response)
	}
	var stats struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage  uint64   `json:"total_usage"`
				PercpuUsage []uint64 `json:"percpu_usage"`
			} `json:"cpu_usage"`
			SystemCPUUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs     uint32 `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemCPUUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64 `json:"usage"`
			Limit uint64 `json:"limit"`
		} `json:"memory_stats"`
		PidsStats struct {
			Current int `json:"current"`
		} `json:"pids_stats"`
		Networks map[string]struct {
			RXBytes uint64 `json:"rx_bytes"`
			TXBytes uint64 `json:"tx_bytes"`
		} `json:"networks"`
		BlkioStats struct {
			IOServiceBytesRecursive []struct {
				Op    string `json:"op"`
				Value uint64 `json:"value"`
			} `json:"io_service_bytes_recursive"`
		} `json:"blkio_stats"`
	}
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		return workerapi.ResourceUsage{}, err
	}
	usage := workerapi.ResourceUsage{
		Name: resource.Name, Kind: resource.Kind, AppID: resource.AppID,
		State: resource.State, MemoryUsageMB: float64(stats.MemoryStats.Usage) / (1024 * 1024),
		MemoryLimitMB: float64(stats.MemoryStats.Limit) / (1024 * 1024),
		PIDs:          stats.PidsStats.Current,
	}
	var cpuDelta, systemDelta uint64
	if stats.CPUStats.CPUUsage.TotalUsage >= stats.PreCPUStats.CPUUsage.TotalUsage {
		cpuDelta = stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage
	}
	if stats.CPUStats.SystemCPUUsage >= stats.PreCPUStats.SystemCPUUsage {
		systemDelta = stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage
	}
	onlineCPUs := stats.CPUStats.OnlineCPUs
	if onlineCPUs == 0 {
		onlineCPUs = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if systemDelta > 0 && cpuDelta > 0 {
		usage.CPUPercent = (float64(cpuDelta) / float64(systemDelta)) * float64(onlineCPUs) * 100
	}
	for _, network := range stats.Networks {
		usage.NetworkRXBytes += network.RXBytes
		usage.NetworkTXBytes += network.TXBytes
	}
	for _, item := range stats.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(item.Op) {
		case "read":
			usage.BlockReadBytes += item.Value
		case "write":
			usage.BlockWriteBytes += item.Value
		}
	}
	return usage, nil
}

func (e *Engine) ContainerLogs(ctx context.Context, name string, tail int) (string, error) {
	path := fmt.Sprintf("/containers/%s/logs?stdout=true&stderr=true&timestamps=true&tail=%d",
		url.PathEscape(name), tail)
	response, err := e.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", engineError(response)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return "", err
	}
	return decodeDockerLogStream(data), nil
}

func (e *Engine) StreamContainerLogs(ctx context.Context, name string, since int64, writer io.Writer) error {
	path := fmt.Sprintf(
		"/containers/%s/logs?follow=true&stdout=true&stderr=true&timestamps=true&since=%d",
		url.PathEscape(name), since)
	response, err := e.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return engineError(response)
	}
	return copyDockerLogStream(response.Body, writer)
}

func copyDockerLogStream(reader io.Reader, writer io.Writer) error {
	header := make([]byte, 8)
	if count, err := io.ReadFull(reader, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			_, writeErr := writer.Write(header[:count])
			return writeErr
		}
		return err
	}
	if (header[0] != 1 && header[0] != 2) ||
		header[1] != 0 || header[2] != 0 || header[3] != 0 {
		if _, err := writer.Write(header); err != nil {
			return err
		}
		_, err := io.Copy(writer, reader)
		return err
	}
	for {
		size := int(header[4])<<24 | int(header[5])<<16 | int(header[6])<<8 | int(header[7])
		if size < 0 || size > 16*1024*1024 {
			return errors.New("invalid Docker log frame")
		}
		if _, err := io.CopyN(writer, reader, int64(size)); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if _, err := io.ReadFull(reader, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
	}
}

func decodeDockerLogStream(data []byte) string {
	if len(data) < 8 || (data[0] != 1 && data[0] != 2) ||
		data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return string(data)
	}
	var output bytes.Buffer
	for len(data) >= 8 {
		size := int(data[4])<<24 | int(data[5])<<16 | int(data[6])<<8 | int(data[7])
		if size < 0 || len(data) < 8+size {
			break
		}
		output.Write(data[8 : 8+size])
		data = data[8+size:]
	}
	return output.String()
}

func (e *Engine) ContainerAction(ctx context.Context, name, action string) error {
	var path string
	method := http.MethodPost
	switch action {
	case "start":
		path = "/containers/" + url.PathEscape(name) + "/start"
	case "stop":
		path = "/containers/" + url.PathEscape(name) + "/stop?t=15"
	case "restart":
		path = "/containers/" + url.PathEscape(name) + "/restart?t=15"
	case "delete":
		path = "/containers/" + url.PathEscape(name) + "?force=true&v=false"
		method = http.MethodDelete
	default:
		return fmt.Errorf("unsupported container action %q", action)
	}
	response, err := e.request(ctx, method, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return engineError(response)
	}
	return nil
}

func (e *Engine) DeleteVolume(ctx context.Context, name string) error {
	response, err := e.request(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name)+"?force=false", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return engineError(response)
	}
	return nil
}

func (e *Engine) ListManaged(ctx context.Context, workstationID string) ([]workerapi.Resource, error) {
	filterValue := map[string][]string{"label": {workerapi.LabelManagedBy + "=" + workerapi.ManagedByValue}}
	if workstationID != "" {
		filterValue["label"] = append(filterValue["label"],
			workerapi.LabelWorkstationID+"="+workstationID)
	}
	filters, _ := json.Marshal(filterValue)
	response, err := e.request(ctx, http.MethodGet,
		"/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, engineError(response)
	}
	var containers []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		State  string            `json:"State"`
		Status string            `json:"Status"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(response.Body).Decode(&containers); err != nil {
		return nil, err
	}
	result := make([]workerapi.Resource, 0, len(containers))
	for _, container := range containers {
		name := ""
		if len(container.Names) > 0 {
			name = strings.TrimPrefix(container.Names[0], "/")
		}
		result = append(result, workerapi.Resource{
			ID: container.ID, Name: name, Kind: container.Labels[workerapi.LabelResourceType],
			WorkstationID: container.Labels[workerapi.LabelWorkstationID],
			AppID:         container.Labels[workerapi.LabelAppID], State: container.State,
			Labels: container.Labels,
		})
	}
	return result, nil
}

func (e *Engine) RemoveWorkstation(ctx context.Context, workstationID string) error {
	resources, err := e.ListManaged(ctx, workstationID)
	if err != nil {
		return err
	}
	sort.SliceStable(resources, func(i, j int) bool {
		return resourceRank(resources[i].Kind) > resourceRank(resources[j].Kind)
	})
	for _, resource := range resources {
		if err := e.ContainerAction(ctx, resource.Name, "delete"); err != nil {
			return fmt.Errorf("delete container %s: %w", resource.Name, err)
		}
	}
	networks, err := e.listManagedNetworks(ctx, workstationID)
	if err != nil {
		return err
	}
	for _, network := range networks {
		if err := e.DeleteNetwork(ctx, network); err != nil {
			return fmt.Errorf("delete network %s: %w", network, err)
		}
	}
	filterValue := map[string][]string{"label": {
		workerapi.LabelManagedBy + "=" + workerapi.ManagedByValue,
		workerapi.LabelWorkstationID + "=" + workstationID,
	}}
	filters, _ := json.Marshal(filterValue)
	response, err := e.request(ctx, http.MethodGet,
		"/volumes?filters="+url.QueryEscape(string(filters)), nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return engineError(response)
	}
	var volumes struct {
		Volumes []struct {
			Name string `json:"Name"`
		} `json:"Volumes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&volumes); err != nil {
		return err
	}
	for _, volume := range volumes.Volumes {
		if err := e.DeleteVolume(ctx, volume.Name); err != nil {
			return fmt.Errorf("delete volume %s: %w", volume.Name, err)
		}
	}
	return nil
}

func (e *Engine) listManagedNetworks(ctx context.Context, workstationID string) ([]string, error) {
	filterValue := map[string][]string{"label": {
		workerapi.LabelManagedBy + "=" + workerapi.ManagedByValue,
		workerapi.LabelWorkstationID + "=" + workstationID,
	}}
	filters, _ := json.Marshal(filterValue)
	response, err := e.request(ctx, http.MethodGet,
		"/networks?filters="+url.QueryEscape(string(filters)), nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, engineError(response)
	}
	var networks []struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(response.Body).Decode(&networks); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(networks))
	for _, network := range networks {
		result = append(result, network.Name)
	}
	return result, nil
}

func (e *Engine) do(ctx context.Context, method, path string, body, output any, expected int) error {
	response, err := e.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		return engineError(response)
	}
	if output != nil {
		return json.NewDecoder(response.Body).Decode(output)
	}
	return nil
}

func (e *Engine) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	if method == "" {
		method = http.MethodDelete
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	return e.client.Do(request)
}

func engineError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	var message struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &message) == nil && message.Message != "" {
		return fmt.Errorf("docker API %s: %s", response.Status, message.Message)
	}
	return fmt.Errorf("docker API %s: %s", response.Status, strings.TrimSpace(string(body)))
}

func SocketExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
