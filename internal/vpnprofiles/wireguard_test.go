package vpnprofiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `[Interface]
PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.0.0.2/32
DNS = 1.1.1.1
MTU = 1320

[Peer]
PublicKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Endpoint = 1.2.3.4:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`

func TestParseCanonicalWireGuardConfiguration(t *testing.T) {
	parsed, err := Parse(validConfig)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Endpoint != "1.2.3.4:51820" ||
		!strings.Contains(parsed.Canonical, "AllowedIPs = 0.0.0.0/0, ::/0") {
		t.Fatalf("unexpected parsed profile: %#v", parsed)
	}
}

func TestParseRejectsScriptsHostnamesAndSplitTunnel(t *testing.T) {
	for name, config := range map[string]string{
		"script":       validConfig + "\nPostUp = touch /tmp/pwned\n",
		"hostname":     strings.Replace(validConfig, "1.2.3.4:51820", "vpn.example.com:51820", 1),
		"split tunnel": strings.Replace(validConfig, "0.0.0.0/0, ::/0", "10.0.0.0/8", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(config); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}

func TestEncryptedStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := Store{
		Directory: filepath.Join(root, "profiles"),
		KeyFile:   filepath.Join(root, "vpn.key"),
	}
	ref, err := store.Save(validConfig)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(store.Directory, ref))),
		"PrivateKey") {
		t.Fatal("encrypted profile contains plaintext key material")
	}
	if parsed, err := Parse(loaded); err != nil || parsed.Endpoint != "1.2.3.4:51820" {
		t.Fatalf("loaded profile invalid: parsed=%#v err=%v", parsed, err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
