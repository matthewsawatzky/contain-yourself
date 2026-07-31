// Package proxy contains controller routing helpers.
package proxy

import (
	"net"
	"strings"
)

// WorkstationHostname extracts the single workstation label before baseDomain.
// It returns an empty string for the controller host, nested labels, IP
// addresses, and unrelated domains.
func WorkstationHostname(hostport, baseDomain string) string {
	host := strings.ToLower(hostport)
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = strings.ToLower(parsedHost)
	}
	baseDomain = strings.ToLower(strings.TrimSuffix(baseDomain, "."))
	if baseDomain == "" {
		return ""
	}
	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) {
		return ""
	}
	label := strings.TrimSuffix(host, suffix)
	if label == "" || strings.Contains(label, ".") {
		return ""
	}
	return label
}
