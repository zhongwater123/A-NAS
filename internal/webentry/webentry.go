// Package webentry tells which Web entry a request reached the Product
// Service through. The local console and the LAN entry (Caddy, ADR 0012)
// share the API's main listener. The internet entry (ADR 0032) has a loopback
// listener of its own, which Caddy's HTTPS site for the device's domain
// proxies to, so a request is known to come from the internet by the socket
// it arrived on rather than by any header.
package webentry

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

type internetKey struct{}

// Internet reports whether r came through the internet entry, and the domain
// that entry serves.
func Internet(r *http.Request) (host string, ok bool) {
	host, ok = r.Context().Value(internetKey{}).(string)
	return host, ok
}

// InternetHandler serves the internet listener: it marks every request as
// the internet entry's and refuses requests that do not name host, the
// entry's domain, since Caddy forwards only that name here.
func InternetHandler(host string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(hostName(r.Host), host) {
			httpjson.WriteError(w, http.StatusMisdirectedRequest, "misdirected_request", "this entry serves only its own domain")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), internetKey{}, host)))
	})
}

func hostName(hostHeader string) string {
	if host, _, err := net.SplitHostPort(hostHeader); err == nil {
		return host
	}
	return hostHeader
}
