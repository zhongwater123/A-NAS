// Package serviceproxy hands an API that runs as a service of its own, the
// photo service (ADR 0011) or the share service (ADR 0023), the requests
// the Product Service admitted. The Product Service has already
// authenticated the browser and checked CSRF on writes; the service gets
// only the session token, never cookies, so it learns nothing it cannot
// confirm with the Host Agent itself.
package serviceproxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"slices"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// SessionHeader carries the browser's session token to the service, and the
// member's to the assistant's change executor.
const SessionHeader = "X-A-NAS-Session"

// AgentTokenHeader carries an agent's token from the Product Service's agent
// posture to the service behind it, which confirms it again (ADR 0040);
// AgentOriginHeader the scheme and host the agent reached the device at.
const (
	//nolint:gosec // G101: the header's name, not a credential.
	AgentTokenHeader  = "X-A-NAS-Agent-Token"
	AgentOriginHeader = "X-A-NAS-Agent-Origin"
)

// ResponseHeaderTimeout is how long a service may take to start answering a
// request it has received in full, such as importing an uploaded video or
// encoding an image to search by. A service that hangs then frees the
// Product Service's request instead of holding it until the browser leaves.
const ResponseHeaderTimeout = 2 * time.Minute

// Unavailable is the error a request answers while the service cannot be
// reached.
type Unavailable struct {
	Code    string
	Message string
}

// New forwards requests to the service listening on socketPath.
func New(socketPath string, unavailable Unavailable, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns: 16, IdleConnTimeout: time.Minute, ResponseHeaderTimeout: ResponseHeaderTimeout,
	}
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = "http"
			request.Out.URL.Host = "service"
			request.Out.Host = "service"
			onlyTheSession(request.In.Context(), request.Out.Header)
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.WarnContext(r.Context(), "service unreachable", "socket", socketPath, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, unavailable.Code, unavailable.Message)
		},
	}
}

// Local hands requests to a service's handler in this process, as
// development runs it, with the headers New would send.
func Local(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		onlyTheSession(r.Context(), r.Header)
		next.ServeHTTP(w, r)
	})
}

type trustedHeaderKey struct{}
type trustedHeader struct{ name, value string }

// WithInternalHeader carries a header constructed by the Product Service
// through the proxy after browser-supplied internal headers are removed.
// Each call adds one; a name given again replaces its value.
func WithInternalHeader(ctx context.Context, name, value string) context.Context {
	headers, _ := ctx.Value(trustedHeaderKey{}).([]trustedHeader)
	headers = append(slices.Clone(headers), trustedHeader{name, value})
	return context.WithValue(ctx, trustedHeaderKey{}, headers)
}

// onlyTheSession replaces what identifies the browser with the session token
// in ctx. The X-A-NAS- headers are the Product Service's to set, so none the
// browser sent reaches a service.
func onlyTheSession(ctx context.Context, header http.Header) {
	header.Del("Cookie")
	header.Del("X-CSRF-Token")
	header.Del("Authorization")
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "x-a-nas-") {
			header.Del(name)
		}
	}
	headers, _ := ctx.Value(trustedHeaderKey{}).([]trustedHeader)
	for _, h := range headers {
		header.Set(h.name, h.value)
	}
	header.Set(SessionHeader, accounts.SessionToken(ctx))
}
