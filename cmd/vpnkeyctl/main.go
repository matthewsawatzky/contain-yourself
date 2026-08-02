// Command vpnkeyctl manages the key that encrypts stored WireGuard profiles.
//
// It works directly on the controller's data directory rather than through the
// HTTP API, because an encryption key must not travel over the network or be
// held by a process that only needs to read profiles. Run it inside the
// controller container, where /data is mounted:
//
//	docker compose exec controller vpnkeyctl status
//	docker compose exec controller vpnkeyctl generate
//	docker compose exec controller vpnkeyctl rotate --new-key <hex>
package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"workstation-manager/internal/vpnprofiles"
)

const usage = `vpnkeyctl manages the VPN profile encryption key.

Commands:
  status                 Report the active key fingerprint and profile count.
  generate               Print a new random key as hex. Store it yourself.
  rotate --new-key HEX   Re-encrypt every stored profile under a new key.
  rotate --new-key-file PATH

Flags:
  --directory PATH   Profile directory (default $VPN_PROFILES_DIRECTORY or /data/vpn-profiles)
  --key-file PATH    Current key file (default $VPN_ENCRYPTION_KEY_FILE or /data/vpn-profiles.key)

The current key is read from $VPN_ENCRYPTION_KEY when set, otherwise from the
key file. After rotating, update whichever of those the controller uses and
restart it, or it will not be able to read the profiles it just re-encrypted.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	directory := flags.String("directory",
		env("VPN_PROFILES_DIRECTORY", "/data/vpn-profiles"), "profile directory")
	keyFile := flags.String("key-file",
		env("VPN_ENCRYPTION_KEY_FILE", "/data/vpn-profiles.key"), "current key file")
	newKey := flags.String("new-key", "", "new key as hex")
	newKeyFile := flags.String("new-key-file", "", "file holding the new 32-byte key")
	if err := flags.Parse(args); err != nil {
		return err
	}

	store := vpnprofiles.Store{Directory: *directory, KeyFile: *keyFile}
	if raw := strings.TrimSpace(os.Getenv("VPN_ENCRYPTION_KEY")); raw != "" {
		key, err := vpnprofiles.ParseKey(raw)
		if err != nil {
			return fmt.Errorf("VPN_ENCRYPTION_KEY: %w", err)
		}
		store.Key = key
	}

	switch command {
	case "status":
		return status(store)
	case "generate":
		return generate()
	case "rotate":
		return rotate(store, *newKey, *newKeyFile)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

func status(store vpnprofiles.Store) error {
	fingerprint, err := store.KeyFingerprint()
	if err != nil {
		return err
	}
	source := "key file " + store.KeyFile
	if len(store.Key) > 0 {
		source = "VPN_ENCRYPTION_KEY"
	}
	count := 0
	entries, err := os.ReadDir(store.Directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".wg.enc") {
			count++
		}
	}
	fmt.Printf("key source:   %s\n", source)
	fmt.Printf("fingerprint:  %s\n", fingerprint)
	fmt.Printf("directory:    %s\n", store.Directory)
	fmt.Printf("profiles:     %d\n", count)
	return nil
}

func generate() error {
	key, err := vpnprofiles.NewKey()
	if err != nil {
		return err
	}
	fmt.Println(hex.EncodeToString(key))
	fmt.Fprintf(os.Stderr,
		"\nfingerprint %s\nStore this value; it is not saved anywhere by this command.\n",
		vpnprofiles.Fingerprint(key))
	return nil
}

func rotate(store vpnprofiles.Store, newKeyHex, newKeyFile string) error {
	if (newKeyHex == "") == (newKeyFile == "") {
		return errors.New("rotate needs exactly one of --new-key or --new-key-file")
	}
	var key []byte
	var err error
	if newKeyHex != "" {
		key, err = vpnprofiles.ParseKey(newKeyHex)
	} else {
		var data []byte
		if data, err = os.ReadFile(newKeyFile); err == nil {
			// Accept either raw bytes or a hex line, so the file can be written
			// by `vpnkeyctl generate > key.hex` or by dd from /dev/urandom.
			if len(data) == vpnprofiles.KeyBytes {
				key = data
			} else {
				key, err = vpnprofiles.ParseKey(string(data))
			}
		}
	}
	if err != nil {
		return err
	}
	result, err := store.Rotate(key)
	if err != nil {
		return fmt.Errorf("rotation aborted, no profiles were changed: %w", err)
	}
	fmt.Printf("rotated %d of %d profiles (%d already under the new key)\n",
		result.Rotated, result.Total, result.AlreadyNew)
	fmt.Printf("old fingerprint: %s\nnew fingerprint: %s\n",
		result.OldKeyPrint, result.NewKeyPrint)
	fmt.Println("\nUpdate VPN_ENCRYPTION_KEY or the key file to the new key, then restart the controller.")
	return nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
