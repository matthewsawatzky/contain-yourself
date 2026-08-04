// Package workerclient provides the controller's typed, authenticated worker
// client.
package workerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"workstation-manager/pkg/workerapi"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), token: token,
		http: &http.Client{Timeout: 15 * time.Minute},
	}
}

func (c *Client) Health(ctx context.Context) error {
	var health workerapi.Health
	return c.do(ctx, http.MethodGet, "/healthz", nil, &health)
}

func (c *Client) ApproveApp(ctx context.Context, app workerapi.AppSpec) (workerapi.AppApprovalStatus, error) {
	var status workerapi.AppApprovalStatus
	err := c.do(ctx, http.MethodPost, "/v1/apps/approve",
		workerapi.AppApproval{App: app}, &status)
	return status, err
}

func (c *Client) Provision(ctx context.Context, request workerapi.ProvisionRequest) (workerapi.WorkstationStatus, error) {
	var status workerapi.WorkstationStatus
	err := c.do(ctx, http.MethodPost, "/v1/workstations", request, &status)
	return status, err
}

func (c *Client) Rebuild(ctx context.Context, request workerapi.ProvisionRequest) (workerapi.WorkstationStatus, error) {
	var status workerapi.WorkstationStatus
	err := c.do(ctx, http.MethodPost,
		"/v1/workstations/"+request.WorkstationID+"/rebuild", request, &status)
	return status, err
}

func (c *Client) Action(ctx context.Context, id, action string) error {
	return c.do(ctx, http.MethodPost, "/v1/workstations/"+id+"/action",
		workerapi.ActionRequest{Action: action}, nil)
}

func (c *Client) Inspect(ctx context.Context, id string) (workerapi.WorkstationStatus, error) {
	var status workerapi.WorkstationStatus
	err := c.do(ctx, http.MethodGet, "/v1/workstations/"+id, nil, &status)
	return status, err
}

func (c *Client) List(ctx context.Context) ([]workerapi.Resource, error) {
	var resources []workerapi.Resource
	err := c.do(ctx, http.MethodGet, "/v1/resources", nil, &resources)
	return resources, err
}

func (c *Client) Usage(ctx context.Context, id string) (workerapi.UsageResponse, error) {
	var usage workerapi.UsageResponse
	err := c.do(ctx, http.MethodGet, "/v1/workstations/"+id+"/usage", nil, &usage)
	return usage, err
}

// Egress reports how a workstation's traffic is actually leaving.
func (c *Client) Egress(ctx context.Context, id string) (workerapi.EgressStatus, error) {
	var status workerapi.EgressStatus
	err := c.do(ctx, http.MethodGet, "/v1/workstations/"+id+"/egress", nil, &status)
	return status, err
}

func (c *Client) Logs(ctx context.Context, id, appID string, tail int) (workerapi.LogResponse, error) {
	var logs workerapi.LogResponse
	err := c.do(ctx, http.MethodGet, fmt.Sprintf(
		"/v1/workstations/%s/apps/%s/logs?tail=%d", id, appID, tail), nil, &logs)
	return logs, err
}

func (c *Client) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("worker request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		var apiError workerapi.Error
		if json.Unmarshal(data, &apiError) == nil && apiError.Error != "" {
			return fmt.Errorf("worker: %s", apiError.Error)
		}
		return fmt.Errorf("worker returned %s", response.Status)
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			return fmt.Errorf("decode worker response: %w", err)
		}
	}
	return nil
}
