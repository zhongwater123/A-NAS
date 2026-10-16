package sharesapi

import (
	"context"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"
)

const SessionHeader = "X-A-NAS-Session"

type Proxy struct{ *httputil.ReverseProxy }

func NewProxy(socketPath string, logger *slog.Logger) *Proxy {
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}, MaxIdleConns: 16, IdleConnTimeout: time.Minute}
	return &Proxy{&httputil.ReverseProxy{Rewrite: func(req *httputil.ProxyRequest) {
		req.Out.URL.Scheme = "http"
		req.Out.URL.Host = "shares"
		req.Out.Host = "shares"
		req.Out.Header.Del("Cookie")
		req.Out.Header.Del("X-CSRF-Token")
		req.Out.Header.Del("Authorization")
		req.Out.Header.Set(SessionHeader, accounts.SessionToken(req.In.Context()))
	}, Transport: transport, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		logger.WarnContext(r.Context(), "share service unreachable", "error", err)
		WriteError(w, 503, "shares_unavailable", "member shares are not available")
	}}}
}
