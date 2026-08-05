package dockerworker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"workstation-manager/internal/manifests"
)

// TestAppCatalogSmokeIntegration provisions every container-service app in
// core_apps/ and app_store/apps/ through the exact same WSLAN topology the
// worker uses in production -- gateway, per-app network sandbox, app
// container joined with container:<sandbox> -- using each app's real,
// unmodified image, command and environment. It proves an app's manifest is
// enough on its own: nothing about the image needs to know WSLAN exists.
func TestAppCatalogSmokeIntegration(t *testing.T) {
	if os.Getenv("DOCKER_INTEGRATION") != "1" {
		t.Skip("set DOCKER_INTEGRATION=1 after building contain-yourself-wslan:dev (./dev build wslan)")
	}
	registry, err := manifests.ScanDirectories(
		filepath.Join("..", "..", "core_apps"),
		filepath.Join("..", "..", "app_store", "apps"),
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine("/var/run/docker.sock")
	for _, entry := range registry.Entries() {
		entry := entry
		if entry.Error != "" {
			t.Fatalf("app %s has an invalid manifest, fix it before it can be smoke tested: %s",
				entry.Manifest.ID, entry.Error)
		}
		if entry.Manifest.Runtime.Type != "container-service" {
			// controller-ui apps have no image or port; workspace-image apps
			// are not network services. Neither goes through WSLAN.
			continue
		}
		t.Run(entry.Manifest.ID, func(t *testing.T) {
			smokeTestApp(t, engine, entry.Manifest)
		})
	}
}

func smokeTestApp(t *testing.T, engine *Engine, app manifests.Manifest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	networkName := "wm-appsmoke-net-" + app.ID + "-" + suffix
	gatewayName := "wm-appsmoke-gw-" + app.ID + "-" + suffix
	sandboxName := "wm-appsmoke-sandbox-" + app.ID + "-" + suffix
	appName := "wm-appsmoke-app-" + app.ID + "-" + suffix
	token := "abcdefghijklmnopqrstuvwxyz012345"
	labels := map[string]string{"managed-by": "workstation-manager-app-smoke", "app-id": app.ID}

	var volumes []string
	defer func() {
		background := context.Background()
		engine.ContainerAction(background, appName, "delete")
		engine.ContainerAction(background, sandboxName, "delete")
		engine.ContainerAction(background, gatewayName, "delete")
		for _, volume := range volumes {
			engine.DeleteVolume(background, volume)
		}
		engine.DeleteNetwork(background, networkName)
	}()

	if err := engine.EnsureImage(ctx, app.Runtime.Image); err != nil {
		t.Fatalf("pull app image %s: %v", app.Runtime.Image, err)
	}
	if err := engine.CreateNetwork(ctx, networkName, labels, false); err != nil {
		t.Fatal(err)
	}
	internal, err := engine.InspectNetwork(ctx, networkName)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: gatewayName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Environment: map[string]string{
			"WSLAN_ROLE": "gateway", "WSLAN_MODE": "direct",
			"WSLAN_INTERNAL_CIDR": internal.Subnet,
			"WSLAN_TOKEN":         token,
			"WSLAN_APPS":          app.ID + "=" + strconv.Itoa(app.Runtime.InternalPort),
		},
		Labels: labels, NetworkMode: "bridge", MemoryBytes: 128 * 1024 * 1024,
		NanoCPUs: 250_000_000, PIDLimit: 128, CapAdd: []string{"NET_ADMIN"},
		Ports: []int{wslanIngressPort}, Sysctls: map[string]string{"net.ipv4.ip_forward": "1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConnectNetwork(ctx, networkName, gatewayName, []string{"wslan-gateway"}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, gatewayName, "start"); err != nil {
		t.Fatal(err)
	}
	if err := waitForWSLANReady(ctx, engine, gatewayName); err != nil {
		logs, _ := engine.ContainerLogs(ctx, gatewayName, 200)
		t.Fatalf("%v\n%s", err, logs)
	}

	internal, err = engine.InspectNetwork(ctx, networkName)
	if err != nil {
		t.Fatal(err)
	}
	gatewayAddress := internal.Containers[gatewayName]
	if gatewayAddress == "" {
		t.Fatal("gateway has no internal address")
	}

	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: sandboxName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Environment: map[string]string{"WSLAN_ROLE": "sandbox", "WSLAN_GATEWAY": gatewayAddress},
		Labels:      labels, NetworkMode: networkName, DNS: []string{gatewayAddress},
		MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
		CapAdd: []string{"NET_ADMIN"}, NetworkAliases: []string{"app-" + app.ID, app.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, sandboxName, "start"); err != nil {
		t.Fatal(err)
	}

	mounts, createdVolumes, err := createSmokeMounts(ctx, engine, app, appName, labels)
	volumes = createdVolumes
	if err != nil {
		t.Fatalf("prepare storage: %v", err)
	}

	memoryMB := app.Resources.DefaultMemoryMB
	if memoryMB <= 0 {
		memoryMB = 256
	}
	cpu := app.Resources.DefaultCPU
	if cpu <= 0 {
		cpu = 0.5
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: appName, Image: app.Runtime.Image,
		Command: app.Runtime.Command, Environment: app.Runtime.Environment,
		Labels: labels, NetworkMode: "container:" + sandboxName,
		MemoryBytes: int64(memoryMB) * 1024 * 1024, NanoCPUs: int64(cpu * 1_000_000_000),
		PIDLimit: 256, ShmSizeBytes: int64(app.Resources.ShmSizeMB) * 1024 * 1024,
		CapAdd: app.Permissions.Capabilities, Mounts: mounts,
	}); err != nil {
		t.Fatalf("create app container: %v", err)
	}
	if err := engine.ContainerAction(ctx, appName, "start"); err != nil {
		t.Fatalf("start app container: %v", err)
	}

	if app.Health.Type == "" {
		time.Sleep(3 * time.Second)
		running, _, _, stateErr := engine.ContainerState(ctx, appName)
		if stateErr != nil || !running {
			logs, _ := engine.ContainerLogs(ctx, appName, 200)
			t.Fatalf("app has no declared health check and is not running: %v\n%s", stateErr, logs)
		}
		return
	}

	requestTimeout := time.Duration(app.Health.TimeoutSeconds) * time.Second
	if requestTimeout <= 0 {
		requestTimeout = 5 * time.Second
	}
	if _, err := probeAppHealthBody(ctx, engine, gatewayName, token, app.ID, app.Health.Path, requestTimeout); err != nil {
		gatewayLogs, _ := engine.ContainerLogs(ctx, gatewayName, 200)
		appLogs, _ := engine.ContainerLogs(ctx, appName, 200)
		t.Fatalf("app %s did not become healthy through WSLAN ingress at %s: %v\n"+
			"gateway logs:\n%s\napp logs:\n%s", app.ID, app.Health.Path, err, gatewayLogs, appLogs)
	}
}

// createSmokeMounts creates one throwaway volume per declared storage entry
// and chowns it exactly the way the worker's storage initializer does, so
// non-root images (browser, files) see the ownership they expect.
func createSmokeMounts(ctx context.Context, engine *Engine, app manifests.Manifest,
	appName string, labels map[string]string) ([]Mount, []string, error) {
	mounts := make([]Mount, 0, len(app.Storage))
	volumes := make([]string, 0, len(app.Storage))
	for index, storage := range app.Storage {
		volumeName := appName + "-vol-" + strconv.Itoa(index)
		if err := engine.CreateVolume(ctx, volumeName, labels); err != nil {
			return nil, volumes, err
		}
		volumes = append(volumes, volumeName)
		mounts = append(mounts, Mount{Source: volumeName, Target: storage.Target})
		if storage.OwnerUID == 0 && storage.OwnerGID == 0 {
			continue
		}
		if err := chownSmokeVolume(ctx, engine, app.Runtime.Image, volumeName, storage.Target,
			storage.OwnerUID, storage.OwnerGID, labels); err != nil {
			return mounts, volumes, err
		}
	}
	return mounts, volumes, nil
}

func chownSmokeVolume(ctx context.Context, engine *Engine, image, volumeName, target string,
	uid, gid int, labels map[string]string) error {
	initName := volumeName + "-init"
	owner := strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
	defer engine.ContainerAction(context.Background(), initName, "delete")
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: initName, Image: image, Entrypoint: []string{"chown"}, Command: []string{"-R", owner, target},
		User: "0:0", Labels: labels, NetworkMode: "none",
		MemoryBytes: 128 * 1024 * 1024, NanoCPUs: 100_000_000, PIDLimit: 64,
		Mounts: []Mount{{Source: volumeName, Target: target}},
	}); err != nil {
		return err
	}
	if err := engine.ContainerAction(ctx, initName, "start"); err != nil {
		return err
	}
	return engine.WaitContainer(ctx, initName)
}

// TestWSLANMultiAppInteropIntegration puts two off-the-shelf, WSLAN-unaware
// apps that happen to share the same internal port on one workstation
// network, exactly like a template combining several catalog apps does. It
// checks two things that the whole "no image edits" claim rests on: the
// gateway routes each by app ID despite the shared port, and one app's
// sandbox can reach the other directly by its stable DNS alias without going
// through the gateway at all -- neither app was told the other exists.
func TestWSLANMultiAppInteropIntegration(t *testing.T) {
	if os.Getenv("DOCKER_INTEGRATION") != "1" {
		t.Skip("set DOCKER_INTEGRATION=1 after building contain-yourself-wslan:dev (./dev build wslan)")
	}
	engine := NewEngine("/var/run/docker.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	networkName := "wm-interop-net-" + suffix
	gatewayName := "wm-interop-gw-" + suffix
	token := "abcdefghijklmnopqrstuvwxyz012345"
	labels := map[string]string{"managed-by": "workstation-manager-interop-integration", "test-id": suffix}
	wslanImage := envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev")
	// The wslan image's Alpine busybox is built without the httpd applet, so the
	// two stub "apps" use the upstream busybox image instead, which has it.
	busyboxImage := envForTest("WSLAN_TEST_BUSYBOX_IMAGE", "busybox:1.37.0")
	if err := engine.EnsureImage(ctx, busyboxImage); err != nil {
		t.Fatalf("pull %s: %v", busyboxImage, err)
	}

	type appUnderTest struct{ id, sandbox, container string }
	apps := []appUnderTest{
		{id: "alpha", sandbox: "wm-interop-sandbox-alpha-" + suffix, container: "wm-interop-app-alpha-" + suffix},
		{id: "beta", sandbox: "wm-interop-sandbox-beta-" + suffix, container: "wm-interop-app-beta-" + suffix},
	}

	defer func() {
		background := context.Background()
		for _, app := range apps {
			engine.ContainerAction(background, app.container, "delete")
			engine.ContainerAction(background, app.sandbox, "delete")
		}
		engine.ContainerAction(background, gatewayName, "delete")
		engine.DeleteNetwork(background, networkName)
	}()

	if err := engine.CreateNetwork(ctx, networkName, labels, false); err != nil {
		t.Fatal(err)
	}
	internal, err := engine.InspectNetwork(ctx, networkName)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: gatewayName, Image: wslanImage,
		Environment: map[string]string{
			"WSLAN_ROLE": "gateway", "WSLAN_MODE": "direct",
			"WSLAN_INTERNAL_CIDR": internal.Subnet, "WSLAN_TOKEN": token,
			// Both apps declare the same internal port, unmodified -- this is
			// the "shared port" case docs/networking.md promises works.
			"WSLAN_APPS": "alpha=3000,beta=3000",
		},
		NetworkMode: "bridge", MemoryBytes: 128 * 1024 * 1024,
		NanoCPUs: 250_000_000, PIDLimit: 128, CapAdd: []string{"NET_ADMIN"},
		Ports: []int{wslanIngressPort}, Sysctls: map[string]string{"net.ipv4.ip_forward": "1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConnectNetwork(ctx, networkName, gatewayName, []string{"wslan-gateway"}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, gatewayName, "start"); err != nil {
		t.Fatal(err)
	}
	if err := waitForWSLANReady(ctx, engine, gatewayName); err != nil {
		logs, _ := engine.ContainerLogs(ctx, gatewayName, 200)
		t.Fatalf("%v\n%s", err, logs)
	}

	internal, err = engine.InspectNetwork(ctx, networkName)
	if err != nil {
		t.Fatal(err)
	}
	gatewayAddress := internal.Containers[gatewayName]
	if gatewayAddress == "" {
		t.Fatal("gateway has no internal address")
	}

	for _, app := range apps {
		if err := engine.CreateContainer(ctx, ContainerConfig{
			Name: app.sandbox, Image: wslanImage,
			Environment: map[string]string{"WSLAN_ROLE": "sandbox", "WSLAN_GATEWAY": gatewayAddress},
			Labels:      labels, NetworkMode: networkName, DNS: []string{gatewayAddress},
			MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
			CapAdd: []string{"NET_ADMIN"}, NetworkAliases: []string{"app-" + app.id, app.id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := engine.ContainerAction(ctx, app.sandbox, "start"); err != nil {
			t.Fatal(err)
		}
		if err := engine.CreateContainer(ctx, ContainerConfig{
			Name: app.container, Image: busyboxImage, Entrypoint: []string{"sh"},
			Command: []string{"-c", fmt.Sprintf(
				"mkdir -p /tmp/www && echo %s >/tmp/www/healthz && exec httpd -f -p 3000 -h /tmp/www", app.id)},
			Labels: labels, NetworkMode: "container:" + app.sandbox,
			MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
		}); err != nil {
			t.Fatal(err)
		}
		if err := engine.ContainerAction(ctx, app.container, "start"); err != nil {
			t.Fatal(err)
		}
	}

	for _, app := range apps {
		body, err := probeAppHealthBody(ctx, engine, gatewayName, token, app.id, "/healthz", 5*time.Second)
		if err != nil {
			t.Fatalf("gateway routing to %s failed: %v", app.id, err)
		}
		if got := lastLogField(body); got != app.id {
			t.Fatalf("gateway routed the %s app ID to the wrong container, got body %q (line: %q)",
				app.id, body, got)
		}
	}

	peerName := "wm-interop-peer-" + suffix
	defer engine.ContainerAction(context.Background(), peerName, "delete")
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: peerName, Image: wslanImage, Entrypoint: []string{"wget"},
		Command: []string{"-q", "-O", "-", "http://app-beta:3000/healthz"},
		Labels:  labels, NetworkMode: "container:" + apps[0].sandbox,
		MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ContainerAction(ctx, peerName, "start"); err != nil {
		t.Fatal(err)
	}
	if err := engine.WaitContainer(ctx, peerName); err != nil {
		logs, _ := engine.ContainerLogs(ctx, peerName, 200)
		t.Fatalf("app alpha could not reach app beta directly by its DNS alias: %v\n%s", err, logs)
	}
}

// waitForWSLANReady polls a gateway container's logs for its startup line.
// The dockerworker package itself never talks to WSLAN over the network
// directly (see waitHTTP's host-container-name dialing, which only resolves
// from inside another container on the same Docker network); tests run on
// the host, so they poll logs and then probe from inside a sibling container.
func waitForWSLANReady(ctx context.Context, engine *Engine, containerName string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		logs, err := engine.ContainerLogs(ctx, containerName, 50)
		if err == nil && strings.Contains(logs, "WSLAN ingress ready") {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("WSLAN gateway %s did not report ready within 20s", containerName)
}

// probeAppHealthBody retries a WSLAN-ingress health request from a throwaway
// container sharing the gateway's network namespace, the same vantage point
// the worker itself probes from, and returns the response body so callers can
// check which app actually answered.
func probeAppHealthBody(ctx context.Context, engine *Engine, gatewayName, token, appID, path string,
	requestTimeout time.Duration) (string, error) {
	deadline := time.Now().Add(90 * time.Second)
	seconds := int(requestTimeout.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	var lastErr error
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		probeName := fmt.Sprintf("%s-probe-%s-%d", gatewayName, appID, attempt)
		body, err := runProbe(ctx, engine, probeName, gatewayName, token, appID, path, seconds)
		if err == nil {
			return body, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	return "", lastErr
}

// lastLogField strips ContainerLogs' leading "<RFC3339Nano timestamp> "
// prefix from a single-line response body, e.g. the httpd stub's echoed app
// ID, so callers can compare against the exact content the app wrote.
func lastLogField(body string) string {
	line := strings.TrimSpace(body)
	if idx := strings.IndexByte(line, ' '); idx >= 0 {
		line = line[idx+1:]
	}
	return strings.TrimSpace(line)
}

func runProbe(ctx context.Context, engine *Engine, probeName, gatewayName, token, appID, path string,
	timeoutSeconds int) (string, error) {
	defer engine.ContainerAction(context.Background(), probeName, "delete")
	if err := engine.CreateContainer(ctx, ContainerConfig{
		Name: probeName, Image: envForTest("WSLAN_TEST_IMAGE", "contain-yourself-wslan:dev"),
		Entrypoint: []string{"wget"},
		Command: []string{
			"-q", "-T", strconv.Itoa(timeoutSeconds), "-O", "-",
			"--header", "X-Contain-WSLAN-Token: " + token,
			"--header", "X-Contain-WSLAN-App: " + appID,
			"http://127.0.0.1:9000" + path,
		},
		NetworkMode: "container:" + gatewayName,
		MemoryBytes: 32 * 1024 * 1024, NanoCPUs: 50_000_000, PIDLimit: 16,
	}); err != nil {
		return "", err
	}
	if err := engine.ContainerAction(ctx, probeName, "start"); err != nil {
		return "", err
	}
	if err := engine.WaitContainer(ctx, probeName); err != nil {
		return "", err
	}
	return engine.ContainerLogs(ctx, probeName, 50)
}
