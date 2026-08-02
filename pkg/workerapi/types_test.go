package workerapi

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The controller and worker are separate processes that ship as separate
// images, so a deployment can briefly run mismatched versions of each. These
// tests pin the JSON field names that cross that boundary: renaming one is a
// compatibility break, and should fail here rather than in production.

func TestProvisionRequestWireFieldNames(t *testing.T) {
	data, err := json.Marshal(ProvisionRequest{
		WorkstationID: "ws-abc123def4", Persistent: true, VPNRequired: true,
		VPNProfile: &VPNProfile{WireGuardConfig: "[Interface]"},
		MemoryMB:   4096, CPU: 2, PIDLimit: 512, WorkspaceImage: "alpine:3.21",
		Apps: []AppSpec{{ID: "terminal"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{
		"workstation_id", "persistent", "vpn_required", "vpn_profile",
		"memory_mb", "cpu", "pid_limit", "workspace_image", "apps",
	} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("ProvisionRequest is missing wire field %q", field)
		}
	}
}

func TestOptionalProvisionFieldsAreOmitted(t *testing.T) {
	data, err := json.Marshal(ProvisionRequest{WorkstationID: "ws-abc123def4"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The worker rejects unknown fields, and treats a present-but-empty
	// workspace image as "seed nothing". Both must stay absent when unset.
	for _, field := range []string{"vpn_profile", "workspace_image"} {
		if _, ok := decoded[field]; ok {
			t.Errorf("field %q should be omitted when unset", field)
		}
	}
}

func TestAppSpecWireFieldNames(t *testing.T) {
	data, err := json.Marshal(AppSpec{
		ID: "terminal", Version: "1.0.0", ManifestSHA256: "ab", Image: "img:1",
		Command: []string{"ttyd"}, Environment: map[string]string{"TZ": "UTC"},
		InternalPort: 7681, MemoryMB: 512, CPU: 0.5, ShmSizeMB: 64,
		Capabilities: []string{"NET_RAW"},
		Storage:      []StorageSpec{{Type: "workspace", Target: "/workspace"}},
		HealthPath:   "/", HealthTimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{
		"id", "version", "manifest_sha256", "image", "command", "environment",
		"internal_port", "memory_mb", "cpu", "shm_size_mb", "capabilities",
		"storage", "health_path", "health_timeout_seconds",
	} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("AppSpec is missing wire field %q", field)
		}
	}
}

func TestProvisionRequestRoundTrip(t *testing.T) {
	original := ProvisionRequest{
		WorkstationID: "ws-abc123def4", Persistent: true, VPNRequired: true,
		VPNProfile: &VPNProfile{WireGuardConfig: "[Interface]\nPrivateKey = x"},
		MemoryMB:   4096, CPU: 2.5, PIDLimit: 512, WorkspaceImage: "alpine:3.21",
		Apps: []AppSpec{{
			ID: "terminal", Image: "tsl0922/ttyd:1.7.7", InternalPort: 7681,
			MemoryMB: 512, CPU: 0.5,
			Storage: []StorageSpec{{Type: "workspace", Target: "/workspace", OwnerUID: 1000, OwnerGID: 1000}},
		}},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored ProvisionRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	// The worker decodes with DisallowUnknownFields; mirror that here so a
	// field this package can emit but not accept is caught.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&restored); err != nil {
		t.Fatalf("decode with unknown fields disallowed: %v", err)
	}
	if restored.WorkspaceImage != original.WorkspaceImage ||
		restored.CPU != original.CPU || restored.PIDLimit != original.PIDLimit {
		t.Fatalf("round trip changed scalars: %+v", restored)
	}
	if restored.VPNProfile == nil ||
		restored.VPNProfile.WireGuardConfig != original.VPNProfile.WireGuardConfig {
		t.Fatalf("round trip lost the VPN profile: %+v", restored.VPNProfile)
	}
	if len(restored.Apps) != 1 || len(restored.Apps[0].Storage) != 1 ||
		restored.Apps[0].Storage[0].OwnerUID != 1000 {
		t.Fatalf("round trip lost app storage: %+v", restored.Apps)
	}
}

func TestLabelConstantsAreStable(t *testing.T) {
	// Reconciliation and teardown find resources purely by these labels, so a
	// change here orphans every container created by an earlier version.
	expected := map[string]string{
		LabelManagedBy: "managed-by", LabelWorkstationID: "workstation-id",
		LabelResourceType: "resource-type", LabelAppID: "app-id",
		ManagedByValue: "workstation-manager",
	}
	for actual, want := range expected {
		if actual != want {
			t.Errorf("label constant = %q, want %q", actual, want)
		}
	}
}
