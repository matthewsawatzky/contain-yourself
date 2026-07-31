package dockerworker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWireGuardSecretFileIntegration(t *testing.T) {
	if os.Getenv("DOCKER_INTEGRATION") != "1" {
		t.Skip("set DOCKER_INTEGRATION=1 to run Docker integration tests")
	}
	engine := NewEngine("/var/run/docker.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	name := fmt.Sprintf("wm-integration-wg-secret-%d", time.Now().UnixNano())
	defer engine.ContainerAction(context.Background(), name, "delete")

	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: name, Image: "qmcgaw/gluetun:v3.40.0",
		Environment: map[string]string{
			"VPN_SERVICE_PROVIDER":      "custom",
			"VPN_TYPE":                  "wireguard",
			"WIREGUARD_CONF_SECRETFILE": wireGuardSecretPath,
		},
		NetworkMode: "bridge", MemoryBytes: 256 * 1024 * 1024,
		NanoCPUs: 250_000_000, PIDLimit: 128,
		CapAdd: []string{"NET_ADMIN"},
		Devices: []map[string]string{{
			"PathOnHost": "/dev/net/tun", "PathInContainer": "/dev/net/tun",
			"CgroupPermissions": "rwm",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.CopyFile(ctx, name, wireGuardSecretDirectory,
		wireGuardSecretFilename, []byte(integrationWireGuardConfig(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, name, "start"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		logs, err := engine.ContainerLogs(ctx, name, 200)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs, "endpoint IP is not set") {
			t.Fatalf("Gluetun did not read its WireGuard secret file:\n%s", logs)
		}
		if strings.Contains(logs, "[wireguard]") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	logs, _ := engine.ContainerLogs(ctx, name, 200)
	t.Fatalf("Gluetun never reached WireGuard startup:\n%s", logs)
}

func integrationWireGuardConfig(t *testing.T) string {
	t.Helper()
	key := func() string {
		value := make([]byte, 32)
		if _, err := rand.Read(value); err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(value)
	}
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.200.0.2/32

[Peer]
PublicKey = %s
Endpoint = 192.0.2.1:51820
AllowedIPs = 0.0.0.0/0
`, key(), key())
}
