package photosapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// SessionHeader carries the browser's session token from the Product Service
// to the photo service, which confirms it with the Host Agent itself.
const SessionHeader = "X-A-NAS-Session"

// NewProxy forwards the photo API to the photo service's Unix socket
// (ADR 0011). The caller has already authenticated the browser and checked
// CSRF on writes; the proxy passes only the session token, never cookies, so
// the photo service learns nothing it cannot verify.
func NewProxy(socketPath string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns: 16, IdleConnTimeout: 60 * time.Second,
	}
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = "http"
			request.Out.URL.Host = "photos"
			request.Out.Host = "photos"
			request.Out.Header.Del("Cookie")
			request.Out.Header.Del("X-CSRF-Token")
			request.Out.Header.Set(SessionHeader, accounts.SessionToken(request.In.Context()))
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.WarnContext(r.Context(), "photo service unreachable", "error", err)
			WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available on this device")
		},
	}
}
