package vpnprofiles

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleConfig = `[Interface]
PrivateKey = qJhBLBBHnJNz9nQnnQhBnJNz9nQnnQhBnJNz9nQnnQg=
Address = 10.2.0.2/32
DNS = 10.2.0.1

[Peer]
PublicKey = mJhBLBBHnJNz9nQnnQhBnJNz9nQnnQhBnJNz9nQnnQg=
Endpoint = 203.0.113.10:51820
AllowedIPs = 0.0.0.0/0
`

func testStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	return Store{
		Directory: filepath.Join(root, "profiles"),
		KeyFile:   filepath.Join(root, "vpn.key"),
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	store := testStore(t)
	ref, err := store.Save(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded, "Endpoint = 203.0.113.10:51820") {
		t.Fatalf("round trip lost content: %q", loaded)
	}
}

func TestStoredProfileIsNotPlaintextOnDisk(t *testing.T) {
	store := testStore(t)
	ref, err := store.Save(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.Directory, ref))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PrivateKey") ||
		strings.Contains(string(data), "203.0.113.10") {
		t.Fatal("profile contents are readable on disk")
	}
	if data[0] != formatV2 {
		t.Fatalf("new profiles should be written as version %d, got %d", formatV2, data[0])
	}
}

func TestKeyFileIsCreatedPrivateAndReused(t *testing.T) {
	store := testStore(t)
	if _, err := store.Save(sampleConfig); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("key file mode = %o, want 600", mode)
	}
	first, err := store.KeyFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.KeyFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("key file was regenerated on a second read")
	}
}

// An operator-supplied key must be used verbatim, and must not cause a key file
// to be written anywhere.
func TestSuppliedKeyTakesPrecedenceAndWritesNoKeyFile(t *testing.T) {
	store := testStore(t)
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	store.Key = key
	ref, err := store.Save(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.KeyFile); !os.IsNotExist(err) {
		t.Fatal("a key file was created even though a key was supplied")
	}
	if _, err := store.Load(ref); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.KeyFingerprint(); got != Fingerprint(key) {
		t.Fatalf("fingerprint = %q, want %q", got, Fingerprint(key))
	}
}

func TestParseKeyAcceptsHexAndRejectsEverythingElse(t *testing.T) {
	key, _ := NewKey()
	parsed, err := ParseKey(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	if string(parsed) != string(key) {
		t.Fatal("ParseKey did not round trip")
	}
	for _, invalid := range []string{
		"", "not-hex", hex.EncodeToString(key[:16]), hex.EncodeToString(append(key, 0)),
	} {
		if _, err := ParseKey(invalid); err == nil {
			t.Errorf("ParseKey(%q) was accepted", invalid)
		}
	}
}

// A wrong key should say so, not surface as an indistinguishable authentication
// failure that looks like data corruption.
func TestWrongKeyReportsAKeyMismatch(t *testing.T) {
	store := testStore(t)
	ref, err := store.Save(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := NewKey()
	wrong := Store{Directory: store.Directory, Key: other}
	_, err = wrong.Load(ref)
	if err == nil {
		t.Fatal("a profile decrypted under the wrong key")
	}
	if !strings.Contains(err.Error(), "different key") {
		t.Fatalf("error = %v, want a key mismatch message", err)
	}
}

// Installations predating the fingerprint header still have version 1 files.
func TestVersion1ProfilesRemainReadable(t *testing.T) {
	store := testStore(t)
	key, _ := NewKey()
	store.Key = key
	if err := os.MkdirAll(store.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	ref := strings.Repeat("ab", 16) + ".wg.enc"
	if err := os.WriteFile(filepath.Join(store.Directory, ref),
		legacySeal(t, key, sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ref)
	if err != nil {
		t.Fatalf("version 1 profile failed to load: %v", err)
	}
	if !strings.Contains(loaded, "203.0.113.10") {
		t.Fatalf("version 1 profile decoded incorrectly: %q", loaded)
	}
}

func TestRotateReEncryptsEveryProfile(t *testing.T) {
	store := testStore(t)
	refs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		ref, err := store.Save(sampleConfig)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	newKey, _ := NewKey()
	result, err := store.Rotate(newKey)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rotated != 3 || result.Total != 3 {
		t.Fatalf("result = %+v, want 3 rotated of 3", result)
	}
	if result.OldKeyPrint == result.NewKeyPrint {
		t.Fatal("rotation reported the same key before and after")
	}

	rotated := Store{Directory: store.Directory, Key: newKey}
	for _, ref := range refs {
		if _, err := rotated.Load(ref); err != nil {
			t.Fatalf("profile %s did not survive rotation: %v", ref, err)
		}
	}
	// The old key must no longer open anything.
	old := Store{Directory: store.Directory, KeyFile: store.KeyFile}
	if _, err := old.Load(refs[0]); err == nil {
		t.Fatal("the old key still decrypts a rotated profile")
	}
}

// Rotation reads every profile before writing any, so one bad file leaves the
// directory exactly as it was.
func TestRotateLeavesEverythingUnchangedWhenOneProfileFails(t *testing.T) {
	store := testStore(t)
	good, err := store.Save(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := strings.Repeat("cd", 16) + ".wg.enc"
	if err := os.WriteFile(filepath.Join(store.Directory, corrupt),
		[]byte{formatV2, 0, 0, 0, 0, 9, 9, 9}, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(store.Directory, good))
	if err != nil {
		t.Fatal(err)
	}
	newKey, _ := NewKey()
	if _, err := store.Rotate(newKey); err == nil {
		t.Fatal("rotation succeeded despite an undecryptable profile")
	}
	after, err := os.ReadFile(filepath.Join(store.Directory, good))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a healthy profile was modified by a failed rotation")
	}
	// No staging files may survive a failure.
	entries, _ := os.ReadDir(store.Directory)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".rotating") {
			t.Fatalf("failed rotation left %s behind", entry.Name())
		}
	}
}

// Re-running a rotation that already completed should be a no-op, not an error.
func TestRotateIsIdempotentUnderTheNewKey(t *testing.T) {
	store := testStore(t)
	if _, err := store.Save(sampleConfig); err != nil {
		t.Fatal(err)
	}
	newKey, _ := NewKey()
	if _, err := store.Rotate(newKey); err != nil {
		t.Fatal(err)
	}
	rotated := Store{Directory: store.Directory, Key: newKey}
	result, err := rotated.Rotate(newKey)
	if err != nil {
		t.Fatalf("re-running a completed rotation failed: %v", err)
	}
	if result.Rotated != 1 || result.Total != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestRotateRejectsAShortKey(t *testing.T) {
	store := testStore(t)
	if _, err := store.Rotate([]byte("too-short")); err == nil {
		t.Fatal("a short key was accepted")
	}
}

func TestLoadRejectsPathTraversalReferences(t *testing.T) {
	store := testStore(t)
	for _, ref := range []string{
		"../../etc/passwd", "..%2f.wg.enc", strings.Repeat("a", 32) + ".txt", "",
	} {
		if _, err := store.Load(ref); err == nil {
			t.Errorf("Load(%q) was accepted", ref)
		}
	}
}

// legacySeal writes the pre-fingerprint version 1 layout.
func legacySeal(t *testing.T, key []byte, plain string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return aead.Seal(append([]byte{formatV1}, nonce...), nonce, []byte(plain), nil)
}
