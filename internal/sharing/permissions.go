// Package sharing defines workstation-scoped share permissions.
package sharing

import (
	"encoding/json"
	"errors"
	"sort"
)

type Permission string

const (
	OpenApps           Permission = "open-apps"
	TerminalControl    Permission = "terminal-control"
	UploadFiles        Permission = "upload-files"
	DownloadFiles      Permission = "download-files"
	RestartWorkstation Permission = "restart-workstation"
	StopWorkstation    Permission = "stop-workstation"
)

var allowed = map[Permission]bool{
	OpenApps: true, TerminalControl: true, UploadFiles: true,
	DownloadFiles: true, RestartWorkstation: true, StopWorkstation: true,
}

func Validate(values []string) ([]Permission, error) {
	seen := make(map[Permission]bool)
	result := make([]Permission, 0, len(values))
	for _, raw := range values {
		permission := Permission(raw)
		if !allowed[permission] {
			return nil, errors.New("unknown share permission: " + raw)
		}
		if !seen[permission] {
			seen[permission] = true
			result = append(result, permission)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("at least one share permission is required")
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func Encode(values []Permission) (string, error) {
	return string(mustJSON(values)), nil
}

func Decode(encoded string) ([]Permission, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, err
	}
	return Validate(values)
}

func Has(values []Permission, wanted Permission) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func Strings(values []Permission) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = string(value)
	}
	return result
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
