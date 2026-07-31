// Package vpnprofiles validates and encrypts user-supplied WireGuard profiles.
package vpnprofiles

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const MaxConfigBytes = 64 * 1024

type Parsed struct {
	Canonical string
	Endpoint  string
}

func Parse(raw string) (Parsed, error) {
	if len(raw) == 0 || len(raw) > MaxConfigBytes {
		return Parsed{}, errors.New("WireGuard configuration must contain 1–65536 bytes")
	}
	sections := map[string]map[string]string{
		"interface": {},
		"peer":      {},
	}
	allowed := map[string]map[string]bool{
		"interface": {
			"privatekey": true, "address": true, "dns": true, "mtu": true,
		},
		"peer": {
			"publickey": true, "presharedkey": true, "endpoint": true,
			"allowedips": true, "persistentkeepalive": true,
		},
	}
	section := ""
	seenSections := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if _, ok := sections[section]; !ok || seenSections[section] {
				return Parsed{}, errors.New("configuration must contain one [Interface] and one [Peer]")
			}
			seenSections[section] = true
			continue
		}
		if section == "" {
			return Parsed{}, errors.New("configuration value appears before a section")
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if !ok || value == "" || !allowed[section][key] {
			return Parsed{}, fmt.Errorf("WireGuard directive %q is not allowed", key)
		}
		if _, duplicate := sections[section][key]; duplicate {
			return Parsed{}, fmt.Errorf("duplicate WireGuard directive %q", key)
		}
		sections[section][key] = value
	}
	if err := scanner.Err(); err != nil {
		return Parsed{}, err
	}
	if !seenSections["interface"] || !seenSections["peer"] {
		return Parsed{}, errors.New("configuration must contain one [Interface] and one [Peer]")
	}
	if err := validateKey("PrivateKey", sections["interface"]["privatekey"], false); err != nil {
		return Parsed{}, err
	}
	if err := validateKey("PublicKey", sections["peer"]["publickey"], false); err != nil {
		return Parsed{}, err
	}
	if err := validateKey("PresharedKey", sections["peer"]["presharedkey"], true); err != nil {
		return Parsed{}, err
	}
	if err := validateCIDRs("Address", sections["interface"]["address"], true); err != nil {
		return Parsed{}, err
	}
	if err := validateCIDRs("AllowedIPs", sections["peer"]["allowedips"], true); err != nil {
		return Parsed{}, err
	}
	if !containsDefaultRoute(sections["peer"]["allowedips"]) {
		return Parsed{}, errors.New("AllowedIPs must contain 0.0.0.0/0 for VPN-only internet routing")
	}
	if dns := sections["interface"]["dns"]; dns != "" {
		for _, value := range splitCSV(dns) {
			if net.ParseIP(value) == nil {
				return Parsed{}, fmt.Errorf("DNS value %q is not an IP address", value)
			}
		}
	}
	endpoint := sections["peer"]["endpoint"]
	host, portRaw, err := net.SplitHostPort(endpoint)
	if err != nil || net.ParseIP(strings.Trim(host, "[]")) == nil {
		return Parsed{}, errors.New("Endpoint must use an IP address and port")
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return Parsed{}, errors.New("Endpoint port is invalid")
	}
	if err := validateNumber("MTU", sections["interface"]["mtu"], 576, 1500); err != nil {
		return Parsed{}, err
	}
	if err := validateNumber("PersistentKeepalive", sections["peer"]["persistentkeepalive"], 0, 65535); err != nil {
		return Parsed{}, err
	}
	return Parsed{Canonical: canonical(sections), Endpoint: endpoint}, nil
}

func validateKey(name, value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("%s must be a 32-byte base64 WireGuard key", name)
	}
	return nil
}

func validateCIDRs(name, raw string, required bool) error {
	if raw == "" && required {
		return fmt.Errorf("%s is required", name)
	}
	for _, value := range splitCSV(raw) {
		if _, _, err := net.ParseCIDR(value); err != nil {
			return fmt.Errorf("%s value %q is not a CIDR", name, value)
		}
	}
	return nil
}

func validateNumber(name, raw string, minimum, maximum int) error {
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return nil
}

func splitCSV(raw string) []string {
	items := strings.Split(raw, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func containsDefaultRoute(raw string) bool {
	for _, value := range splitCSV(raw) {
		if value == "0.0.0.0/0" {
			return true
		}
	}
	return false
}

func canonical(sections map[string]map[string]string) string {
	var output strings.Builder
	output.WriteString("[Interface]\n")
	writeKeys(&output, sections["interface"], []string{"privatekey", "address", "dns", "mtu"})
	output.WriteString("\n[Peer]\n")
	writeKeys(&output, sections["peer"], []string{
		"publickey", "presharedkey", "endpoint", "allowedips", "persistentkeepalive",
	})
	return output.String()
}

func writeKeys(output *strings.Builder, values map[string]string, keys []string) {
	names := map[string]string{
		"privatekey": "PrivateKey", "address": "Address", "dns": "DNS", "mtu": "MTU",
		"publickey": "PublicKey", "presharedkey": "PresharedKey", "endpoint": "Endpoint",
		"allowedips": "AllowedIPs", "persistentkeepalive": "PersistentKeepalive",
	}
	for _, key := range keys {
		if value := values[key]; value != "" {
			fmt.Fprintf(output, "%s = %s\n", names[key], value)
		}
	}
}
