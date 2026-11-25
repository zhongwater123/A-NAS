package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/localorigin"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/webentry"
)

// Answers of the agent posture that only its guard gives.
const (
	agentBrowserRefused = "agent routes take no requests from a browser"
	agentHostRefused    = "the request does not name this device"
)

// guardAgent enforces the agent posture (ADR 0040): a member's agent token as
// a bearer token and never a session cookie, so no CSRF token either. A
// request a browser sends carries Origin and is refused, as is one whose Host
// does not name the device (DNS rebinding). A client that keeps presenting
// bad tokens waits as one that keeps failing to sign in does. The token goes
// on to the service, which confirms it with the Host Agent again; the
// workspace is the only scope there is.
func (h *productHandler) guardAgent(route apiroute.Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.Accounts == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "accounts_unavailable", "account service is unavailable")
			return
		}
		if r.Header.Get("Origin") != "" {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", agentBrowserRefused)
			return
		}
		if _, internet := webentry.Internet(r); !internet && !localorigin.DeviceHost(r.Host) {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", agentHostRefused)
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			refuseAgent(w, "an agent token is required")
			return
		}
		caller, err := h.Accounts.ResolveAgentToken(withClientOrigin(r), token, accountid.AgentScopeWorkspace)
		if throttled, ok := errors.AsType[*accounts.ThrottledError](err); ok {
			w.Header().Set("Retry-After", throttled.RetryAfterSeconds())
			httpjson.WriteError(w, http.StatusTooManyRequests, "too_many_attempts", "too many invalid agent tokens from this client; try again later")
			return
		}
		if errors.Is(err, accounts.ErrAgentTokenInvalid) {
			refuseAgent(w, "the agent token is invalid or revoked; connect the agent again")
			return
		}
		if err != nil {
			httpjson.WriteInternal(w, r, h.Logger, err)
			return
		}
		scheme := "http"
		if overHTTPS(r) {
			scheme = "https"
		}
		ctx := serviceproxy.WithInternalHeader(r.Context(), serviceproxy.AgentTokenHeader, token)
		ctx = serviceproxy.WithInternalHeader(ctx, serviceproxy.AgentOriginHeader, scheme+"://"+r.Host)
		route.Handler(w, r.WithContext(ctx), accounts.Session{User: caller.User})
	}
}

// bearerToken is the token of an Authorization: Bearer header.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// refuseAgent answers 401 the way an MCP client recognizes: a bearer token
// is what is missing, and no OAuth server stands behind it.
func refuseAgent(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="A-NAS"`)
	httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", message)
}
