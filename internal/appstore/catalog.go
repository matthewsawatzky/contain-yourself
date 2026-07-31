// Package appstore validates source app-store packages and builds the compact
// index consumed by controllers.
package appstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"workstation-manager/internal/manifests"
)

const SchemaVersion = 1

const (
	maxBundleBytes  = 256 * 1024
	maxFileBytes    = 10 * 1024 * 1024
	maxPayloadBytes = 25 * 1024 * 1024
)

type Bundle struct {
	SchemaVersion int           `json:"schema_version"`
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	Summary       string        `json:"summary"`
	Description   string        `json:"description"`
	Categories    []string      `json:"categories"`
	Authors       []Author      `json:"authors"`
	License       License       `json:"license"`
	Links         Links         `json:"links"`
	Compatibility Compatibility `json:"compatibility"`
	Icon          string        `json:"icon"`
	Screenshots   []string      `json:"screenshots"`
	Package       Package       `json:"package"`
}

type Author struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type License struct {
	Catalog  string `json:"catalog"`
	Upstream string `json:"upstream"`
}

type Links struct {
	Homepage string `json:"homepage"`
	Source   string `json:"source"`
	Support  string `json:"support"`
}

type Compatibility struct {
	MinimumControllerVersion string   `json:"minimum_controller_version"`
	Platforms                []string `json:"platforms"`
}

type Package struct {
	PayloadSHA256    string       `json:"payload_sha256"`
	PayloadSizeBytes int64        `json:"payload_size_bytes"`
	Files            []FileRecord `json:"files"`
}

type FileRecord struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type Index struct {
	SchemaVersion int          `json:"schema_version"`
	Apps          []IndexEntry `json:"apps"`
}

type IndexEntry struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	Summary       string          `json:"summary"`
	Categories    []string        `json:"categories"`
	Icon          string          `json:"icon"`
	Image         string          `json:"image"`
	Compatibility Compatibility   `json:"compatibility"`
	Permissions   PermissionBrief `json:"permissions"`
	Bundle        FileRecord      `json:"bundle"`
}

type PermissionBrief struct {
	Network      string   `json:"network"`
	Storage      []string `json:"storage"`
	Capabilities []string `json:"capabilities"`
}

var (
	identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	version    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	digest     = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Build validates every package below root/apps. When update is true it also
// refreshes the generated integrity fields in bundle.json.
func Build(root string, update bool) (Index, error) {
	appsRoot := filepath.Join(root, "apps")
	registry, err := manifests.Scan(appsRoot)
	if err != nil {
		return Index{}, err
	}
	items, err := os.ReadDir(appsRoot)
	if err != nil {
		return Index{}, fmt.Errorf("read store apps: %w", err)
	}
	result := Index{SchemaVersion: SchemaVersion, Apps: []IndexEntry{}}
	for _, item := range items {
		if !item.IsDir() {
			continue
		}
		packageDirectory := filepath.Join(appsRoot, item.Name())
		bundlePath := filepath.Join(packageDirectory, "bundle.json")
		bundle, err := loadBundle(bundlePath)
		if err != nil {
			return Index{}, fmt.Errorf("%s: %w", item.Name(), err)
		}
		manifest, ok := registry.Get(bundle.ID)
		if !ok {
			return Index{}, fmt.Errorf("%s: app.yaml is missing or invalid", item.Name())
		}
		if err := validateBundle(bundle, manifest, item.Name(), packageDirectory); err != nil {
			return Index{}, fmt.Errorf("%s: %w", item.Name(), err)
		}
		payload, err := inspectPayload(packageDirectory)
		if err != nil {
			return Index{}, fmt.Errorf("%s: %w", item.Name(), err)
		}
		if !reflect.DeepEqual(bundle.Package, payload) {
			if !update {
				return Index{}, fmt.Errorf("%s: package hashes are stale; run storectl build", item.Name())
			}
			bundle.Package = payload
			if err := writeJSON(bundlePath, bundle); err != nil {
				return Index{}, err
			}
		}
		bundleRecord, err := inspectFile(root, bundlePath)
		if err != nil {
			return Index{}, err
		}
		bundleRecord.Path = filepath.ToSlash(filepath.Join("apps", item.Name(), "bundle.json"))
		storage := make([]string, 0, len(manifest.Storage))
		seenStorage := make(map[string]bool)
		for _, value := range manifest.Storage {
			if !seenStorage[value.Type] {
				storage = append(storage, value.Type)
				seenStorage[value.Type] = true
			}
		}
		sort.Strings(storage)
		result.Apps = append(result.Apps, IndexEntry{
			ID: bundle.ID, Name: bundle.Name, Version: bundle.Version,
			Summary: bundle.Summary, Categories: bundle.Categories,
			Icon:  filepath.ToSlash(filepath.Join("apps", item.Name(), bundle.Icon)),
			Image: manifest.Runtime.Image, Compatibility: bundle.Compatibility,
			Permissions: PermissionBrief{
				Network: manifest.Network.Mode, Storage: storage,
				Capabilities: append([]string{}, manifest.Permissions.Capabilities...),
			},
			Bundle: bundleRecord,
		})
	}
	sort.Slice(result.Apps, func(i, j int) bool { return result.Apps[i].ID < result.Apps[j].ID })
	return result, nil
}

func CheckIndex(root string) error {
	index, err := Build(root, false)
	if err != nil {
		return err
	}
	expected, err := marshalJSON(index)
	if err != nil {
		return err
	}
	actual, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		return fmt.Errorf("read index.json: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("index.json is stale; run storectl build")
	}
	return nil
}

func WriteIndex(root string, index Index) error {
	return writeJSON(filepath.Join(root, "index.json"), index)
}

func loadBundle(path string) (Bundle, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Bundle{}, fmt.Errorf("read bundle.json: %w", err)
	}
	if info.Size() > maxBundleBytes {
		return Bundle{}, fmt.Errorf("bundle.json exceeds %d bytes", maxBundleBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Bundle{}, fmt.Errorf("read bundle.json: %w", err)
	}
	return DecodeBundle(data)
}

func DecodeBundle(data []byte) (Bundle, error) {
	if len(data) > maxBundleBytes {
		return Bundle{}, fmt.Errorf("bundle.json exceeds %d bytes", maxBundleBytes)
	}
	var bundle Bundle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return Bundle{}, fmt.Errorf("decode bundle.json: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func DecodeIndex(data []byte) (Index, error) {
	if len(data) > 1024*1024 {
		return Index{}, errors.New("index.json exceeds 1048576 bytes")
	}
	var index Index
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return Index{}, fmt.Errorf("decode index.json: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Index{}, err
	}
	if index.SchemaVersion != SchemaVersion {
		return Index{}, fmt.Errorf("index schema_version must be %d", SchemaVersion)
	}
	if len(index.Apps) > 1000 {
		return Index{}, errors.New("index contains more than 1000 apps")
	}
	seen := make(map[string]bool)
	for _, app := range index.Apps {
		if !identifier.MatchString(app.ID) || seen[app.ID] {
			return Index{}, fmt.Errorf("invalid or duplicate app id %q", app.ID)
		}
		seen[app.ID] = true
		if strings.TrimSpace(app.Name) == "" || !version.MatchString(app.Version) ||
			len(strings.TrimSpace(app.Summary)) < 8 {
			return Index{}, fmt.Errorf("app %q has invalid display metadata", app.ID)
		}
		if app.Bundle.Path != filepath.ToSlash(filepath.Join("apps", app.ID, "bundle.json")) ||
			!digest.MatchString(app.Bundle.SHA256) || app.Bundle.SizeBytes < 2 ||
			app.Bundle.SizeBytes > maxBundleBytes {
			return Index{}, fmt.Errorf("app %q has invalid bundle metadata", app.ID)
		}
		if app.Icon != filepath.ToSlash(filepath.Join("apps", app.ID, filepath.Base(app.Icon))) {
			return Index{}, fmt.Errorf("app %q has an unsafe icon path", app.ID)
		}
	}
	return index, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("bundle.json contains multiple JSON values")
		}
		return fmt.Errorf("decode bundle.json: %w", err)
	}
	return nil
}

func validateBundle(bundle Bundle, manifest manifests.Manifest, directory string, packageDirectory string) error {
	if bundle.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d", SchemaVersion)
	}
	if !identifier.MatchString(bundle.ID) || bundle.ID != directory || manifest.ID != bundle.ID {
		return errors.New("directory, bundle id, and manifest id must match")
	}
	if bundle.Name != manifest.Name || bundle.Version != manifest.Version || !version.MatchString(bundle.Version) {
		return errors.New("bundle name/version must match the manifest and use semantic versioning")
	}
	if len(strings.TrimSpace(bundle.Summary)) < 8 || len(bundle.Summary) > 100 {
		return errors.New("summary must be between 8 and 100 characters")
	}
	if len(strings.TrimSpace(bundle.Description)) < 20 || len(bundle.Description) > 2000 {
		return errors.New("description must be between 20 and 2000 characters")
	}
	if len(bundle.Categories) == 0 || len(bundle.Categories) > 8 {
		return errors.New("one to eight categories are required")
	}
	for _, category := range bundle.Categories {
		if !identifier.MatchString(category) {
			return fmt.Errorf("invalid category %q", category)
		}
	}
	if len(bundle.Authors) == 0 {
		return errors.New("at least one author is required")
	}
	for _, author := range bundle.Authors {
		if strings.TrimSpace(author.Name) == "" {
			return errors.New("author name is required")
		}
		if author.URL != "" && !validHTTPSURL(author.URL) {
			return fmt.Errorf("invalid author URL %q", author.URL)
		}
	}
	if bundle.License.Catalog == "" || bundle.License.Upstream == "" {
		return errors.New("catalog and upstream license information are required")
	}
	for _, value := range []string{bundle.Links.Homepage, bundle.Links.Source, bundle.Links.Support} {
		if !validHTTPSURL(value) {
			return fmt.Errorf("invalid HTTPS link %q", value)
		}
	}
	if !version.MatchString(bundle.Compatibility.MinimumControllerVersion) {
		return errors.New("minimum_controller_version must use semantic versioning")
	}
	if len(bundle.Compatibility.Platforms) == 0 {
		return errors.New("at least one platform is required")
	}
	for _, platform := range bundle.Compatibility.Platforms {
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return fmt.Errorf("unsupported platform %q", platform)
		}
	}
	if manifest.Runtime.Type != "container-service" {
		return errors.New("store packages must use the container-service runtime")
	}
	if bundle.Icon == "" || bundle.Icon != manifest.Desktop.Icon {
		return errors.New("bundle icon must match desktop.icon in app.yaml")
	}
	for _, path := range append([]string{bundle.Icon}, bundle.Screenshots...) {
		if err := validateRelativeFile(packageDirectory, path); err != nil {
			return err
		}
	}
	return nil
}

func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.Fragment == ""
}

func validateRelativeFile(root string, path string) error {
	clean := filepath.Clean(path)
	if path == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe package path %q", path)
	}
	info, err := os.Lstat(filepath.Join(root, clean))
	if err != nil {
		return fmt.Errorf("package file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("package file %q must be a regular file", path)
	}
	return nil
}

func inspectPayload(root string) (Package, error) {
	var files []FileRecord
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed: %s", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular package file: %s", relative)
		}
		if filepath.ToSlash(relative) == "bundle.json" {
			return nil
		}
		record, err := inspectFile(root, path)
		if err != nil {
			return err
		}
		record.Path = filepath.ToSlash(relative)
		files = append(files, record)
		return nil
	})
	if err != nil {
		return Package{}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files) == 0 {
		return Package{}, errors.New("package payload is empty")
	}
	payloadHash := sha256.New()
	var size int64
	for _, file := range files {
		fmt.Fprintf(payloadHash, "%s\n%s\n%d\n", file.Path, file.SHA256, file.SizeBytes)
		size += file.SizeBytes
		if size > maxPayloadBytes {
			return Package{}, fmt.Errorf("package payload exceeds %d bytes", maxPayloadBytes)
		}
	}
	return Package{
		PayloadSHA256:    hex.EncodeToString(payloadHash.Sum(nil)),
		PayloadSizeBytes: size, Files: files,
	}, nil
}

func inspectFile(root string, path string) (FileRecord, error) {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return FileRecord{}, err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return FileRecord{}, err
	}
	relative, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return FileRecord{}, errors.New("file escaped catalogue root")
	}
	info, err := os.Stat(cleanPath)
	if err != nil {
		return FileRecord{}, err
	}
	if info.Size() > maxFileBytes {
		return FileRecord{}, fmt.Errorf("package file %q exceeds %d bytes", relative, maxFileBytes)
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return FileRecord{}, err
	}
	sum := sha256.Sum256(data)
	return FileRecord{
		Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(data)),
	}, nil
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeJSON(path string, value any) error {
	data, err := marshalJSON(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
