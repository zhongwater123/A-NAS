// Package localorigin holds request checks shared by endpoints that change
// host state or open a shell. Browsers reach the product API on loopback,
// directly on the local console or through the LAN entry (ADR 0012), so
// requests must name this device by loopback name or IP address and, from a
// browser, come from the Web desktop's own origin. Through the internet entry
// (ADR 0032) they name the entry's domain instead.
package localorigin

import (
	"errors"
	"mime"
	"net"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/webentry"
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

// namesDevice reports whether r's Host names this device as the entry it came
// through serves it. The internet entry's listener already refuses other
// names; its domain is accepted nowhere else.
func namesDevice(r *http.Request) bool {
	if domain, ok := webentry.Internet(r); ok {
		return strings.EqualFold(hostName(r.Host), domain)
	}
	return DeviceHost(r.Host)
}

func hostName(hostHeader string) string {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// crossOrigin is the standard library's check (#272): a browser's
// Sec-Fetch-Site must say same-origin, and without it the Origin, when there
// is one, must name the request's host.
var crossOrigin = http.NewCrossOriginProtection()

// CheckWrite accepts a state-changing request only when it names this device
// (DeviceHost, or the internet entry's domain on that entry), carries a JSON
// body (which forces a CORS preflight that the API never grants) and, from a
// browser, comes from the same origin.
func CheckWrite(r *http.Request) error {
	if !namesDevice(r) {
		return ErrHost
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return ErrContentType
	}
	if crossOrigin.Check(r) != nil {
		return ErrOrigin
	}
	return nil
}
