// Package localorigin holds request checks shared by endpoints that change
// host state. The product API has no authentication yet, so state-changing
// requests must come from the Web desktop itself on a loopback address.
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
	ErrHost        = errors.New("request host is not a loopback endpoint")
	ErrOrigin      = errors.New("request origin does not match host")
	ErrContentType = errors.New("request body must be application/json")
)

// LoopbackHost reports whether a Host header names a loopback endpoint.
// Rejecting other names stops DNS-rebound pages that resolve to 127.0.0.1.
func LoopbackHost(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CheckWrite accepts a state-changing request only when it targets a loopback
// host, carries a JSON body (which forces a CORS preflight that the API never
// grants) and, when the browser sends an Origin, that origin is the same host.
func CheckWrite(r *http.Request) error {
	if !LoopbackHost(r.Host) {
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
