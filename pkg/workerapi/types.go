// Package workerapi defines the narrow protocol shared by the controller and
// Docker worker. It intentionally has no Docker-specific escape hatches.
package workerapi

import "time"

const (
	LabelManagedBy     = "managed-by"
	LabelWorkstationID = "workstation-id"
	LabelResourceType  = "resource-type"
	LabelAppID         = "app-id"
	ManagedByValue     = "workstation-manager"
)

type Error struct {
	Error string `json:"error"`
}

type Health struct {
	Status string `json:"status"`
	Docker string `json:"docker,omitempty"`
}

type AppSpec struct {
	ID                   string            `json:"id"`
	Version              string            `json:"version,omitempty"`
	ManifestSHA256       string            `json:"manifest_sha256,omitempty"`
	Image                string            `json:"image"`
	Command              []string          `json:"command,omitempty"`
	Environment          map[string]string `json:"environment,omitempty"`
	InternalPort         int               `json:"internal_port"`
	MemoryMB             int               `json:"memory_mb"`
	CPU                  float64           `json:"cpu"`
	ShmSizeMB            int               `json:"shm_size_mb,omitempty"`
	Capabilities         []string          `json:"capabilities,omitempty"`
	Storage              []StorageSpec     `json:"storage,omitempty"`
	HealthPath           string            `json:"health_path,omitempty"`
	HealthTimeoutSeconds int               `json:"health_timeout_seconds,omitempty"`
}

// AppApproval is the complete, narrowly scoped app specification an
// administrator approved through the controller app store.
type AppApproval struct {
	App AppSpec `json:"app"`
}

type AppApprovalStatus struct {
	ID             string    `json:"id"`
	Version        string    `json:"version"`
	Image          string    `json:"image"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	Specification  string    `json:"specification_sha256"`
	ApprovedAt     time.Time `json:"approved_at"`
}

type StorageSpec struct {
	Type     string `json:"type"`
	Target   string `json:"target"`
	OwnerUID int    `json:"owner_uid,omitempty"`
	OwnerGID int    `json:"owner_gid,omitempty"`
}

type ProvisionRequest struct {
	WorkstationID string      `json:"workstation_id"`
	Persistent    bool        `json:"persistent"`
	VPNRequired   bool        `json:"vpn_required"`
	VPNProfile    *VPNProfile `json:"vpn_profile,omitempty"`
	MemoryMB      int         `json:"memory_mb"`
	CPU           float64     `json:"cpu"`
	PIDLimit      int         `json:"pid_limit"`
	// WorkspaceImage seeds the shared workspace volume once, on first
	// creation. It is not a long-lived container: the worker runs it to
	// completion and removes it. Empty leaves the workspace empty.
	WorkspaceImage string `json:"workspace_image,omitempty"`
	// EgressMode selects how the WSLAN gateway forwards traffic. Empty is
	// treated as "derive from VPNRequired" for controllers older than the
	// named modes.
	EgressMode string    `json:"egress_mode,omitempty"`
	Apps       []AppSpec `json:"apps"`
}

// VPNProfile carries one validated WireGuard profile over the authenticated
// controller-to-worker connection. The worker injects it as a file rather than
// adding secrets to the Docker container environment.
type VPNProfile struct {
	WireGuardConfig string `json:"wireguard_config"`
}

type ActionRequest struct {
	Action string `json:"action"`
}

// EgressStatus is what a workstation's gateway reports about how its traffic is
// actually leaving, as opposed to how it was configured to leave.
//
// Everything here is read from the gateway's own kernel state. Nothing contacts
// an outside service to determine it, which matters for a workstation whose
// point is not to be observed.
type EgressStatus struct {
	WorkstationID string `json:"workstation_id"`
	// Mode is the configured egress mode the gateway is running.
	Mode string `json:"mode"`
	// Healthy is the gateway's own verdict. For WireGuard this is false when
	// the tunnel is down, which is also when the gateway is refusing to
	// forward: the mode fails closed.
	Healthy bool `json:"healthy"`
	// FailsClosed records whether losing the tunnel stops traffic rather than
	// letting it fall back to the host's connection.
	FailsClosed bool `json:"fails_closed"`
	// Tunnel is present only for tunnelled modes.
	Tunnel *TunnelStatus `json:"tunnel,omitempty"`
	// Error explains why the status could not be collected, if it could not.
	Error string `json:"error,omitempty"`
}

// TunnelStatus describes a live WireGuard tunnel. It deliberately carries no
// key material: the gateway parses only peer records, never the interface line
// that holds the private key.
type TunnelStatus struct {
	Up bool `json:"up"`
	// Endpoint is the peer's address, which is the exit the workstation's
	// traffic appears to come from.
	Endpoint string `json:"endpoint,omitempty"`
	// HandshakeAgeSeconds is -1 when a handshake has never completed.
	HandshakeAgeSeconds int64  `json:"handshake_age_seconds"`
	ReceivedBytes       uint64 `json:"received_bytes"`
	SentBytes           uint64 `json:"sent_bytes"`
}

type Resource struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`
	WorkstationID string            `json:"workstation_id"`
	AppID         string            `json:"app_id,omitempty"`
	State         string            `json:"state"`
	Health        string            `json:"health,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	CreatedAt     time.Time         `json:"created_at,omitempty"`
}

type WorkstationStatus struct {
	WorkstationID string     `json:"workstation_id"`
	State         string     `json:"state"`
	VPNState      string     `json:"vpn_state,omitempty"`
	Resources     []Resource `json:"resources"`
}

type ResourceUsage struct {
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	AppID           string  `json:"app_id,omitempty"`
	State           string  `json:"state"`
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryUsageMB   float64 `json:"memory_usage_mb"`
	MemoryLimitMB   float64 `json:"memory_limit_mb"`
	PIDs            int     `json:"pids"`
	NetworkRXBytes  uint64  `json:"network_rx_bytes"`
	NetworkTXBytes  uint64  `json:"network_tx_bytes"`
	BlockReadBytes  uint64  `json:"block_read_bytes"`
	BlockWriteBytes uint64  `json:"block_write_bytes"`
	Error           string  `json:"error,omitempty"`
}

type UsageResponse struct {
	WorkstationID string          `json:"workstation_id"`
	Resources     []ResourceUsage `json:"resources"`
}

type LogResponse struct {
	WorkstationID string `json:"workstation_id"`
	AppID         string `json:"app_id"`
	Lines         int    `json:"lines"`
	Logs          string `json:"logs"`
}
