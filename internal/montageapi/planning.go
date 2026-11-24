package montageapi

import (
	"context"
	"log/slog"
	"net/http"
	"slices"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// A planning grant (docs/architecture/montage-center.md, 规划授权): the
// requests that may lead to planning register one for the member, behind
// their session, before the Product Service forwards them; the montage
// center is told by AssistantHeader whether VNAS can plan.

// AssistantHeader tells the montage center that the Product Service
// registered a planning grant for the member behind this request ("on"), or
// that they have no VNAS to plan with ("off"). Only RegisterPlan sets it: the
// proxy to the montage service drops every X-A-NAS- header a browser sent
// and adds this one from the request's context (serviceproxy).
const AssistantHeader = "X-A-NAS-Assistant"

// PlanningRoutes are the requests that register a planning grant.
var PlanningRoutes = []apiroute.Endpoint{
	{Method: http.MethodPost, Pattern: PathPrefix + "/projects/{projectID}/generate"},
	{Method: http.MethodPost, Pattern: PathPrefix + "/jobs/{jobID}/replan"},
	{Method: http.MethodPost, Pattern: PathPrefix + "/jobs/{jobID}/drop-assets"},
	{Method: http.MethodPost, Pattern: PathPrefix + "/jobs/{jobID}/retry-analysis"},
}

// ProductEndpoints are the endpoints the Product Service forwards as they
// are: all but the planning routes and the song's.
func ProductEndpoints() []apiroute.Endpoint {
	return slices.DeleteFunc(Endpoints(), func(e apiroute.Endpoint) bool { return slices.Contains(PlanningRoutes, e) })
}

// PlanRegistrar registers a planning grant for the member signed in on the
// context's session: the File Broker's assistant_plan.
type PlanRegistrar interface {
	RegisterAssistantPlan(ctx context.Context) (string, error)
}

// RegisterPlan registers a planning grant before forwarding to next, and
// says so with AssistantHeader. A device or member without VNAS goes on
// without one; a broker that cannot register answers 503, forwarding
// nothing.
func RegisterPlan(registrar PlanRegistrar, next http.Handler, logger *slog.Logger) apiroute.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		on := "off"
		if registrar != nil {
			// The route puts the session's token on the request's context;
			// a resolved Session does not carry it.
			plan, err := registrar.RegisterAssistantPlan(r.Context())
			if err != nil {
				logger.WarnContext(r.Context(), "planning grant not registered", "error", err)
				httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "VNAS cannot take the plan right now; try again later")
				return
			}
			if plan == wire.PlanRegistered {
				on = "on"
			}
		}
		ctx := serviceproxy.WithInternalHeader(accounts.WithUser(r.Context(), session.User), AssistantHeader, on)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}
