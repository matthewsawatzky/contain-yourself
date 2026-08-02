package vpnprofiles

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeyBytes is the AES-256 key length every profile is sealed under.
const KeyBytes = 32

// Stored profile layout:
//
//	version 1: [0x01][nonce][ciphertext]
//	version 2: [0x02][key fingerprint (4)][nonce][ciphertext]
//
// Version 2 adds the fingerprint so a profile sealed under a different key
// reports that plainly instead of surfacing as an indistinguishable
// authentication failure. Version 1 is still read so existing installations
// keep working; anything written from now on is version 2.
const (
	formatV1          = 1
	formatV2          = 2
	keyFingerprintLen = 4
)

type Store struct {
	Directory string
	KeyFile   string
	// Key, when set, is used directly and KeyFile is never read or created.
	// This is how an operator brings their own key.
	Key []byte
}

// ParseKey decodes a hex-encoded 32-byte key supplied by an operator.
func ParseKey(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, errors.New("VPN encryption key must be hex-encoded")
	}
	if len(decoded) != KeyBytes {
		return nil, fmt.Errorf("VPN encryption key must decode to exactly %d bytes, got %d",
			KeyBytes, len(decoded))
	}
	return decoded, nil
}

// NewKey generates a fresh random key for an operator to store.
func NewKey() ([]byte, error) {
	key := make([]byte, KeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// Fingerprint identifies a key without revealing it, for error messages and
// the stored header.
func Fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:keyFingerprintLen])
}

func (s Store) Save(config string) (string, error) {
	parsed, err := Parse(config)
	if err != nil {
		return "", err
	}
	key, err := s.key()
	if err != nil {
		return "", err
	}
	data, err := seal(key, parsed.Canonical)
	if err != nil {
		return "", err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(idBytes) + ".wg.enc"
	if err := os.MkdirAll(s.Directory, 0o700); err != nil {
		return "", fmt.Errorf("create VPN profile directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.Directory, ref), data, 0o600); err != nil {
		return "", fmt.Errorf("write encrypted VPN profile: %w", err)
	}
	return ref, nil
}

func (s Store) Load(ref string) (string, error) {
	if !validRef(ref) {
		return "", errors.New("invalid VPN profile reference")
	}
	key, err := s.key()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(s.Directory, ref))
	if err != nil {
		return "", fmt.Errorf("read encrypted VPN profile: %w", err)
	}
	plain, err := open(key, data)
	if err != nil {
		return "", err
	}
	if _, err := Parse(plain); err != nil {
		return "", fmt.Errorf("stored VPN profile is invalid: %w", err)
	}
	return plain, nil
}

func (s Store) Remove(ref string) {
	if validRef(ref) {
		_ = os.Remove(filepath.Join(s.Directory, ref))
	}
}

func seal(key []byte, plain string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	fingerprint, err := hex.DecodeString(Fingerprint(key))
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, 1+keyFingerprintLen+len(nonce)+len(plain)+aead.Overhead())
	data = append(data, formatV2)
	data = append(data, fingerprint...)
	data = append(data, nonce...)
	return aead.Seal(data, nonce, []byte(plain), nil), nil
}

func open(key, data []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < 1 {
		return "", errors.New("encrypted VPN profile is malformed")
	}
	var offset int
	switch data[0] {
	case formatV1:
		offset = 1
	case formatV2:
		offset = 1 + keyFingerprintLen
		if len(data) < offset {
			return "", errors.New("encrypted VPN profile is malformed")
		}
		expected, err := hex.DecodeString(Fingerprint(key))
		if err != nil {
			return "", err
		}
		if subtle.ConstantTimeCompare(data[1:offset], expected) != 1 {
			return "", fmt.Errorf(
				"VPN profile was encrypted under a different key (profile %s, configured %s)",
				hex.EncodeToString(data[1:offset]), Fingerprint(key))
		}
	default:
		return "", errors.New("encrypted VPN profile has an unknown format version")
	}
	if len(data) < offset+aead.NonceSize() {
		return "", errors.New("encrypted VPN profile is malformed")
	}
	nonce := data[offset : offset+aead.NonceSize()]
	plain, err := aead.Open(nil, nonce, data[offset+aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("decrypt VPN profile")
	}
	return string(plain), nil
}

// key resolves the encryption key. An operator-supplied key wins; otherwise the
// key file is read, and generated on first use if absent.
func (s Store) key() ([]byte, error) {
	if len(s.Key) > 0 {
		if len(s.Key) != KeyBytes {
			return nil, fmt.Errorf("VPN encryption key must contain exactly %d bytes", KeyBytes)
		}
		return s.Key, nil
	}
	if s.KeyFile == "" {
		return nil, errors.New("no VPN encryption key or key file is configured")
	}
	if data, err := os.ReadFile(s.KeyFile); err == nil {
		if len(data) != KeyBytes {
			return nil, fmt.Errorf("VPN encryption key file must contain exactly %d bytes", KeyBytes)
		}
		return data, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.KeyFile), 0o700); err != nil {
		return nil, err
	}
	key, err := NewKey()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.KeyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return s.key()
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(key); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

// KeyFingerprint reports which key this store is configured to use, so an
// operator can confirm a rotation took effect.
func (s Store) KeyFingerprint() (string, error) {
	key, err := s.key()
	if err != nil {
		return "", err
	}
	return Fingerprint(key), nil
}

// RotationResult reports what a rotation did.
type RotationResult struct {
	Rotated     int
	AlreadyNew  int
	Total       int
	OldKeyPrint string
	NewKeyPrint string
}

// Rotate re-encrypts every stored profile under newKey.
//
// It decrypts every profile before writing anything, so a wrong old key or one
// corrupt file aborts the whole operation with the directory untouched. New
// files are staged alongside the originals and renamed in place only after all
// of them are written, which keeps the window where the directory could hold a
// mix of keys as small as the renames themselves.
func (s Store) Rotate(newKey []byte) (RotationResult, error) {
	result := RotationResult{}
	if len(newKey) != KeyBytes {
		return result, fmt.Errorf("new VPN encryption key must contain exactly %d bytes", KeyBytes)
	}
	oldKey, err := s.key()
	if err != nil {
		return result, err
	}
	result.OldKeyPrint, result.NewKeyPrint = Fingerprint(oldKey), Fingerprint(newKey)
	entries, err := os.ReadDir(s.Directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, err
	}

	type staged struct{ target, temporary string }
	var pending []staged
	cleanup := func() {
		for _, item := range pending {
			_ = os.Remove(item.temporary)
		}
	}

	for _, entry := range entries {
		if entry.IsDir() || !validRef(entry.Name()) {
			continue
		}
		result.Total++
		path := filepath.Join(s.Directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			cleanup()
			return result, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		plain, err := open(oldKey, data)
		if err != nil {
			// Already under the new key? Then this is a resumed rotation.
			if _, newErr := open(newKey, data); newErr == nil {
				result.AlreadyNew++
				continue
			}
			cleanup()
			return result, fmt.Errorf("decrypt %s: %w", entry.Name(), err)
		}
		sealed, err := seal(newKey, plain)
		if err != nil {
			cleanup()
			return result, fmt.Errorf("re-encrypt %s: %w", entry.Name(), err)
		}
		temporary := path + ".rotating"
		if err := os.WriteFile(temporary, sealed, 0o600); err != nil {
			cleanup()
			return result, fmt.Errorf("stage %s: %w", entry.Name(), err)
		}
		pending = append(pending, staged{target: path, temporary: temporary})
	}

	for _, item := range pending {
		if err := os.Rename(item.temporary, item.target); err != nil {
			return result, fmt.Errorf("replace %s: %w", filepath.Base(item.target), err)
		}
		result.Rotated++
	}
	return result, nil
}

func validRef(ref string) bool {
	return strings.HasSuffix(ref, ".wg.enc") && len(ref) == 32+len(".wg.enc") &&
		!strings.ContainsAny(ref, `/\`)
}
