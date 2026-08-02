package workerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workstation-manager/pkg/workerapi"
)

const testToken = "test-worker-token-0123456789"

func TestClientAuthenticatesEveryRequest(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		writeTestJSON(w, http.StatusOK, workerapi.Health{Status: "ok"})
	}))
	defer server.Close()

	if err := New(server.URL, testToken).Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if authorization != "Bearer "+testToken {
		t.Fatalf("Authorization = %q, want bearer token", authorization)
	}
}

func TestClientTrimsTrailingSlashFromBaseURL(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeTestJSON(w, http.StatusOK, workerapi.Health{Status: "ok"})
	}))
	defer server.Close()

	if err := New(server.URL+"/", testToken).Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if path != "/healthz" {
		t.Fatalf("path = %q, want /healthz without a doubled slash", path)
	}
}

func TestProvisionSendsRequestAndDecodesStatus(t *testing.T) {
	var received workerapi.ProvisionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workstations" {
			t.Errorf("got %s %s, want POST /v1/workstations", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode provision request: %v", err)
		}
		writeTestJSON(w, http.StatusCreated, workerapi.WorkstationStatus{
			WorkstationID: received.WorkstationID, State: "running",
		})
	}))
	defer server.Close()

	status, err := New(server.URL, testToken).Provision(context.Background(),
		workerapi.ProvisionRequest{
			WorkstationID:  "ws-abc123def4",
			WorkspaceImage: "alpine:3.21",
			CPU:            2, MemoryMB: 4096, PIDLimit: 512,
			Apps: []workerapi.AppSpec{{ID: "terminal", Image: "tsl0922/ttyd:1.7.7"}},
		})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if status.State != "running" || status.WorkstationID != "ws-abc123def4" {
		t.Fatalf("status = %+v, want running ws-abc123def4", status)
	}
	if received.WorkspaceImage != "alpine:3.21" {
		t.Fatalf("workspace image did not survive the wire: %q", received.WorkspaceImage)
	}
	if len(received.Apps) != 1 || received.Apps[0].ID != "terminal" {
		t.Fatalf("apps did not survive the wire: %+v", received.Apps)
	}
}

func TestRebuildTargetsTheWorkstationPath(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeTestJSON(w, http.StatusOK, workerapi.WorkstationStatus{State: "running"})
	}))
	defer server.Close()

	_, err := New(server.URL, testToken).Rebuild(context.Background(),
		workerapi.ProvisionRequest{WorkstationID: "ws-abc123def4"})
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if path != "/v1/workstations/ws-abc123def4/rebuild" {
		t.Fatalf("path = %q", path)
	}
}

func TestLogsBuildsTailQuery(t *testing.T) {
	var target string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = r.URL.RequestURI()
		writeTestJSON(w, http.StatusOK, workerapi.LogResponse{Logs: "hello"})
	}))
	defer server.Close()

	logs, err := New(server.URL, testToken).Logs(context.Background(), "ws-abc123def4", "terminal", 50)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if logs.Logs != "hello" {
		t.Fatalf("logs = %q", logs.Logs)
	}
	if target != "/v1/workstations/ws-abc123def4/apps/terminal/logs?tail=50" {
		t.Fatalf("target = %q", target)
	}
}

func TestClientSurfacesStructuredWorkerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, http.StatusUnprocessableEntity,
			workerapi.Error{Error: `image "evil:latest" is not approved`})
	}))
	defer server.Close()

	_, err := New(server.URL, testToken).Provision(context.Background(),
		workerapi.ProvisionRequest{WorkstationID: "ws-abc123def4"})
	if err == nil {
		t.Fatal("expected an error for a 422 response")
	}
	if !strings.Contains(err.Error(), "is not approved") {
		t.Fatalf("error = %v, want the worker's message preserved", err)
	}
}

func TestClientFallsBackToStatusWhenBodyIsNotAnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer server.Close()

	err := New(server.URL, testToken).Action(context.Background(), "ws-abc123def4", "stop")
	if err == nil {
		t.Fatal("expected an error for a 502 response")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v, want the HTTP status", err)
	}
}

func TestActionSendsTheRequestedAction(t *testing.T) {
	var request workerapi.ActionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode action: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := New(server.URL, testToken).Action(context.Background(), "ws-abc123def4", "restart"); err != nil {
		t.Fatalf("Action: %v", err)
	}
	if request.Action != "restart" {
		t.Fatalf("action = %q", request.Action)
	}
}

func TestClientRejectsUndecodableSuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not json"))
	}))
	defer server.Close()

	_, err := New(server.URL, testToken).Usage(context.Background(), "ws-abc123def4")
	if err == nil || !strings.Contains(err.Error(), "decode worker response") {
		t.Fatalf("err = %v, want a decode failure", err)
	}
}

func TestContextCancellationIsPropagated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(server.URL, testToken).Health(ctx); err == nil {
		t.Fatal("expected the cancelled context to fail the request")
	}
}

func writeTestJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
