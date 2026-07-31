package vpnprofiles

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Store struct {
	Directory string
	KeyFile   string
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
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, []byte(parsed.Canonical), nil)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(idBytes) + ".wg.enc"
	if err := os.MkdirAll(s.Directory, 0o700); err != nil {
		return "", fmt.Errorf("create VPN profile directory: %w", err)
	}
	data := append([]byte{1}, nonce...)
	data = append(data, sealed...)
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
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	if len(data) < 1+aead.NonceSize() || data[0] != 1 {
		return "", errors.New("encrypted VPN profile is malformed")
	}
	plain, err := aead.Open(nil, data[1:1+aead.NonceSize()], data[1+aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("decrypt VPN profile")
	}
	if _, err := Parse(string(plain)); err != nil {
		return "", fmt.Errorf("stored VPN profile is invalid: %w", err)
	}
	return string(plain), nil
}

func (s Store) Remove(ref string) {
	if validRef(ref) {
		_ = os.Remove(filepath.Join(s.Directory, ref))
	}
}

func (s Store) key() ([]byte, error) {
	if data, err := os.ReadFile(s.KeyFile); err == nil {
		if len(data) != 32 {
			return nil, errors.New("VPN encryption key must contain exactly 32 bytes")
		}
		return data, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.KeyFile), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
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

func validRef(ref string) bool {
	return strings.HasSuffix(ref, ".wg.enc") && len(ref) == 32+len(".wg.enc") &&
		!strings.ContainsAny(ref, `/\`)
}
