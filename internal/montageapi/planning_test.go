package montageapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

type registrar struct {
	plan  string
	err   error
	token string
}

func (r *registrar) RegisterAssistantPlan(ctx context.Context) (string, error) {
	r.token = accounts.SessionToken(ctx)
	return r.plan, r.err
}

// The requests that may lead to planning register a planning grant behind
// the member's session first, and only the Product Service says so: the
// header reaches the montage center through the proxy, which drops the one a
// browser sent.
func TestPlanningRequestsRegisterAGrantFirst(t *testing.T) {
	var seen []string
	next := serviceproxy.Local(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get(montageapi.AssistantHeader))
	}))
	// As the Product Service's routes give it: the Session resolved from the
	// cookie has no Token, which is on the request's context alone.
	session := accounts.Session{User: accounts.User{ID: "user:alice", Username: "alice"}}
	for _, c := range []struct {
		name   string
		reg    *registrar
		status int
		header string
	}{
		{"registered", &registrar{plan: wire.PlanRegistered}, http.StatusOK, "on"},
		{"no VNAS on the device", &registrar{plan: wire.PlanDeviceOff}, http.StatusOK, "off"},
		{"no VNAS for the account", &registrar{plan: wire.PlanNoAssistant}, http.StatusOK, "off"},
		{"the broker cannot register", &registrar{err: errors.New("unavailable")}, http.StatusServiceUnavailable, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			seen = nil
			request := httptest.NewRequest(http.MethodPost, "/api/v1/montage/jobs/job:1/replan", nil)
			request = request.WithContext(accounts.WithSessionToken(request.Context(), "session-token"))
			// The browser's own header is never believed.
			request.Header.Set(montageapi.AssistantHeader, "on")
			recorder := httptest.NewRecorder()
			montageapi.RegisterPlan(c.reg, next, nil)(recorder, request, session)
			if recorder.Code != c.status || c.reg.token != "session-token" {
				t.Fatalf("status %d, token %q", recorder.Code, c.reg.token)
			}
			if c.header == "" && len(seen) != 0 || c.header != "" && !slices.Equal(seen, []string{c.header}) {
				t.Fatalf("forwarded with %q", seen)
			}
		})
	}
	// The planning routes are the Product Service's own, not forwarded as they are.
	for _, route := range montageapi.PlanningRoutes {
		if slices.Contains(montageapi.ProductEndpoints(), route) || !slices.Contains(montageapi.Endpoints(), route) {
			t.Fatalf("route %v", route)
		}
	}
}
