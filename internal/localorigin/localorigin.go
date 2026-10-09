// Package localorigin holds request checks shared by endpoints that change
// host state or open a shell. Browsers reach the product API on loopback,
// directly on the local console or through the LAN entry (ADR 0012), so
// requests must name this device by loopback name or IP address and, from a
// browser, come from the Web desktop's own origin.
package localorigin

import (
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var (
	ErrHost        = errors.New("request host does not name this device by address")
	ErrOrigin      = errors.New("request origin does not match host")
	ErrContentType = errors.New("request body must be application/json")
)

// LoopbackHost reports whether a Host header names a loopback endpoint.
func LoopbackHost(hostHeader string) bool {
	host := hostName(hostHeader)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DeviceHost reports whether a Host header names this device the way its
// browsers do: localhost on the local console, an IP address from the LAN.
// A DNS-rebound page sends its own domain name, so every other name is
// rejected; an IP address cannot be rebound.
func DeviceHost(hostHeader string) bool {
	host := hostName(hostHeader)
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil
}

func hostName(hostHeader string) string {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// CheckWrite accepts a state-changing request only when it names this device
// (DeviceHost), carries a JSON body (which forces a CORS preflight that the
// API never grants) and, when the browser sends an Origin, that origin is the
// same host.
func CheckWrite(r *http.Request) error {
	if !DeviceHost(r.Host) {
		return ErrHost
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return ErrContentType
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			return ErrOrigin
		}
	}
	return nil
}
