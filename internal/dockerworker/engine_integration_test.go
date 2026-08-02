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

func TestWSLANGatewayIntegration(t *testing.T) {
	if os.Getenv("DOCKER_INTEGRATION") != "1" {
		t.Skip("set DOCKER_INTEGRATION=1 after building contain-yourself-wslan:dev")
	}
	engine := NewEngine("/var/run/docker.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	containerName := "wm-integration-wslan-" + suffix
	sandboxName := "wm-integration-sandbox-" + suffix
	appName := "wm-integration-app-" + suffix
	probeName := "wm-integration-probe-" + suffix
	networkName := "wm-integration-network-" + suffix
	labels := map[string]string{
		"managed-by": "workstation-manager-integration",
		"test-id":    suffix,
	}
	defer engine.DeleteNetwork(context.Background(), networkName)
	defer engine.ContainerAction(context.Background(), containerName, "delete")
	defer engine.ContainerAction(context.Background(), sandboxName, "delete")
	defer engine.ContainerAction(context.Background(), appName, "delete")
	defer engine.ContainerAction(context.Background(), probeName, "delete")

	if err := engine.CreateNetwork(ctx, networkName, labels, false); err != nil {
		t.Fatal(err)
	}
	internal, err := engine.InspectNetwork(ctx, networkName)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: containerName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Environment: map[string]string{
			"WSLAN_ROLE": "gateway", "WSLAN_MODE": "direct",
			"WSLAN_INTERNAL_CIDR": internal.Subnet,
			"WSLAN_TOKEN":         "abcdefghijklmnopqrstuvwxyz012345",
			"WSLAN_APPS":          "testapp=3000",
		},
		NetworkMode: "bridge", MemoryBytes: 128 * 1024 * 1024,
		NanoCPUs: 250_000_000, PIDLimit: 128, CapAdd: []string{"NET_ADMIN"},
		Ports: []int{wslanIngressPort}, Sysctls: map[string]string{"net.ipv4.ip_forward": "1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConnectNetwork(ctx, networkName, containerName, []string{"wslan-gateway"}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, containerName, "start"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		logs, logErr := engine.ContainerLogs(ctx, containerName, 50)
		if logErr == nil && strings.Contains(logs, "WSLAN ingress ready") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	internal, err = engine.InspectNetwork(ctx, networkName)
	if err != nil {
		t.Fatal(err)
	}
	gatewayAddress := internal.Containers[containerName]
	if gatewayAddress == "" {
		t.Fatal("gateway has no private WSLAN address")
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: sandboxName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Environment: map[string]string{
			"WSLAN_ROLE": "sandbox", "WSLAN_GATEWAY": gatewayAddress,
		},
		Labels: labels, NetworkMode: networkName, DNS: []string{gatewayAddress},
		MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
		CapAdd: []string{"NET_ADMIN"}, NetworkAliases: []string{"app-testapp"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, sandboxName, "start"); err != nil {
		t.Fatal(err)
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: appName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Entrypoint: []string{"sh"},
		Command: []string{"-c",
			"mkdir -p /tmp/www && echo ok >/tmp/www/healthz && exec httpd -f -p 3000 -h /tmp/www"},
		Labels: labels, NetworkMode: "container:" + sandboxName,
		MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, appName, "start"); err != nil {
		t.Fatal(err)
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: probeName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Entrypoint: []string{"wget"},
		Command: []string{
			"-q", "-O", "-",
			"--header", "X-Contain-WSLAN-Token: abcdefghijklmnopqrstuvwxyz012345",
			"--header", "X-Contain-WSLAN-App: testapp",
			"http://127.0.0.1:9000/healthz",
		},
		Labels: labels, NetworkMode: "container:" + containerName,
		MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, probeName, "start"); err != nil {
		t.Fatal(err)
	}
	if err := engine.WaitContainer(ctx, probeName); err != nil {
		gatewayLogs, _ := engine.ContainerLogs(ctx, containerName, 200)
		probeLogs, _ := engine.ContainerLogs(ctx, probeName, 200)
		t.Fatalf("WSLAN health probe failed: %v\ngateway:\n%s\nprobe:\n%s",
			err, gatewayLogs, probeLogs)
	}
}

func TestWSLANWireGuardConfigIntegration(t *testing.T) {
	if os.Getenv("DOCKER_INTEGRATION") != "1" {
		t.Skip("set DOCKER_INTEGRATION=1 after building contain-yourself-wslan:dev")
	}
	engine := NewEngine("/var/run/docker.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	containerName := "wm-integration-wg-" + suffix
	probeName := "wm-integration-wg-probe-" + suffix
	networkName := "wm-integration-wg-network-" + suffix
	labels := map[string]string{"test-id": suffix}
	defer engine.DeleteNetwork(context.Background(), networkName)
	defer engine.ContainerAction(context.Background(), containerName, "delete")
	defer engine.ContainerAction(context.Background(), probeName, "delete")

	if err := engine.CreateNetwork(ctx, networkName, labels, false); err != nil {
		t.Fatal(err)
	}
	internal, err := engine.InspectNetwork(ctx, networkName)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: containerName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Environment: map[string]string{
			"WSLAN_ROLE": "gateway", "WSLAN_MODE": "wireguard",
			"WSLAN_INTERNAL_CIDR":        internal.Subnet,
			"WSLAN_TOKEN":                "abcdefghijklmnopqrstuvwxyz012345",
			"WSLAN_APPS":                 "",
			"WSLAN_SKIP_HANDSHAKE_CHECK": "1",
		},
		NetworkMode: "bridge", MemoryBytes: 128 * 1024 * 1024,
		NanoCPUs: 250_000_000, PIDLimit: 128, CapAdd: []string{"NET_ADMIN"},
		Ports: []int{wslanIngressPort}, Sysctls: map[string]string{"net.ipv4.ip_forward": "1"},
		Devices: []map[string]string{{
			"PathOnHost": "/dev/net/tun", "PathInContainer": "/dev/net/tun",
			"CgroupPermissions": "rwm",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConnectNetwork(ctx, networkName, containerName, []string{"wslan-gateway"}); err != nil {
		t.Fatal(err)
	}
	if err := engine.CopyFile(ctx, containerName, wireGuardSecretDirectory,
		wireGuardSecretFilename, []byte(integrationWireGuardConfig(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, containerName, "start"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		logs, logErr := engine.ContainerLogs(ctx, containerName, 50)
		if logErr == nil && strings.Contains(logs, "WSLAN ingress ready") {
			break
		}
		running, _, _, stateErr := engine.ContainerState(ctx, containerName)
		if stateErr == nil && !running {
			t.Fatalf("WireGuard WSLAN exited:\n%s", logs)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: probeName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Entrypoint: []string{"wget"},
		Command:    []string{"-q", "-O", "-", "http://127.0.0.1:9000/healthz"},
		Labels:     labels, NetworkMode: "container:" + containerName,
		MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, probeName, "start"); err != nil {
		t.Fatal(err)
	}
	if err := engine.WaitContainer(ctx, probeName); err != nil {
		gatewayLogs, _ := engine.ContainerLogs(ctx, containerName, 200)
		t.Fatalf("WireGuard WSLAN health probe failed: %v\n%s", err, gatewayLogs)
	}
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
DNS = 1.1.1.1

[Peer]
PublicKey = %s
Endpoint = 192.0.2.1:51820
AllowedIPs = 0.0.0.0/0
`, key(), key())
}

func envForTest(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
