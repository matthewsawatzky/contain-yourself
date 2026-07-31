package dockerworker

import (
	"log/slog"
	"strings"
	"testing"

	"workstation-manager/internal/config"
	"workstation-manager/pkg/workerapi"
)

func testService() *Service {
	return NewService(config.Worker{
		Token:         "abcdefghijklmnopqrstuvwxyz012345",
		AllowedImages: map[string]struct{}{"example/app:1.0.0": {}},
	}, nil, slog.Default())
}

func TestDecodeDockerLogStream(t *testing.T) {
	frame := func(stream byte, value string) []byte {
		size := len(value)
		return append([]byte{stream, 0, 0, 0, byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)}, []byte(value)...)
	}
	data := append(frame(1, "stdout\n"), frame(2, "stderr\n")...)
	if actual := decodeDockerLogStream(data); actual != "stdout\nstderr\n" {
		t.Fatalf("decoded logs = %q", actual)
	}
	if actual := decodeDockerLogStream([]byte("raw tty logs\n")); !strings.Contains(actual, "raw tty") {
		t.Fatalf("raw logs = %q", actual)
	}
}

func validProvision() workerapi.ProvisionRequest {
	return workerapi.ProvisionRequest{
		WorkstationID: "ws-abcdef1234", Persistent: true,
		CPU: 2, MemoryMB: 4096, PIDLimit: 512,
		Apps: []workerapi.AppSpec{{
			ID: "terminal", Image: "example/app:1.0.0", InternalPort: 7681,
			CPU: 0.5, MemoryMB: 512,
			Storage:    []workerapi.StorageSpec{{Type: "workspace", Target: "/workspace"}},
			HealthPath: "/",
		}},
	}
}

func TestProvisionValidation(t *testing.T) {
	service := testService()
	if err := service.validateProvision(validProvision()); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionRejectsUnapprovedImageHostPathAndCapability(t *testing.T) {
	service := testService()
	request := validProvision()
	request.Apps[0].Image = "attacker/image:1"
	if service.validateProvision(request) == nil {
		t.Fatal("unapproved image was accepted")
	}
	request = validProvision()
	request.Apps[0].Storage[0].Target = "../../etc"
	if service.validateProvision(request) == nil {
		t.Fatal("relative storage target was accepted")
	}
	request = validProvision()
	request.Apps[0].Capabilities = []string{"SYS_ADMIN"}
	if service.validateProvision(request) == nil {
		t.Fatal("unsafe capability was accepted")
	}
	request = validProvision()
	request.Apps[0].Environment = map[string]string{"DOCKER_HOST": "unix:///var/run/docker.sock"}
	if service.validateProvision(request) == nil {
		t.Fatal("unsafe environment key was accepted")
	}
	request = validProvision()
	request.Apps[0].ShmSizeMB = 2048
	if service.validateProvision(request) == nil {
		t.Fatal("shared memory larger than app memory was accepted")
	}
}

func TestVPNRequiresUploadedProfile(t *testing.T) {
	service := testService()
	request := validProvision()
	request.VPNRequired = true
	if service.validateProvision(request) == nil {
		t.Fatal("VPN workstation was accepted without an uploaded profile")
	}
}

func TestVPNProfileMustContainSafeWireGuardConfiguration(t *testing.T) {
	service := testService()
	request := validProvision()
	request.VPNRequired = true
	request.VPNProfile = &workerapi.VPNProfile{
		WireGuardConfig: testWireGuardConfig(),
	}
	if err := service.validateProvision(request); err != nil {
		t.Fatalf("valid VPN profile rejected: %v", err)
	}
	request.VPNProfile.WireGuardConfig += "\nPostUp = touch /tmp/unsafe\n"
	if service.validateProvision(request) == nil {
		t.Fatal("unsafe WireGuard directive was accepted")
	}
}

func TestWireGuardConfigurationUsesGluetunSecretFile(t *testing.T) {
	if wireGuardSecretPath != "/tmp/workstation-manager-wireguard.conf" {
		t.Fatalf("WireGuard secret path = %q", wireGuardSecretPath)
	}
	if wireGuardSecretDirectory+"/"+wireGuardSecretFilename != wireGuardSecretPath {
		t.Fatal("WireGuard copy target and Gluetun secret path diverged")
	}
}

func testWireGuardConfig() string {
	return `[Interface]
PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.0.0.2/32
DNS = 1.1.1.1

[Peer]
PublicKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Endpoint = 1.2.3.4:51820
AllowedIPs = 0.0.0.0/0, ::/0
`
}
