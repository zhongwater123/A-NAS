package httpapi

import (
	"net/http"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
)

func TestInvalidPostureCannotRegisterARoute(t *testing.T) {
	for _, posture := range []apiroute.Posture{apiroute.Unset, -1, apiroute.Device + 1} {
		for _, method := range []string{"", http.MethodGet, http.MethodPost} {
			route := apiroute.Route{Method: method, Pattern: "/test", Posture: posture}
			if err := checkPosture(route); err == nil {
				t.Errorf("accepted %s with posture %d", method, posture)
			}
		}
	}
}

// A device route only reads, one method at a time: a screen's token must
// never change anything.
func TestDeviceRoutesOnlyRead(t *testing.T) {
	for _, method := range []string{"", http.MethodPost, http.MethodPut, http.MethodDelete} {
		if err := checkPosture(apiroute.Route{Method: method, Pattern: "/test", Posture: apiroute.Device}); err == nil {
			t.Errorf("accepted a device route for %q", method)
		}
	}
	if err := checkPosture(apiroute.Route{Method: http.MethodGet, Pattern: "/test", Posture: apiroute.Device}); err != nil {
		t.Errorf("refused a device route that reads: %v", err)
	}
}

// An agent route names its method: a mount would let every method of a
// prefix skip the session and the CSRF token.
func TestAgentRoutesAreNeverMounted(t *testing.T) {
	if err := checkPosture(apiroute.Route{Pattern: "/test/", Posture: apiroute.Agent, Endpoints: []apiroute.Endpoint{{Method: http.MethodPost, Pattern: "/test/x"}}}); err == nil {
		t.Error("accepted a mounted agent route")
	}
	if err := checkPosture(apiroute.Route{Method: http.MethodPost, Pattern: "/test", Posture: apiroute.Agent}); err != nil {
		t.Errorf("refused an agent route with its method: %v", err)
	}
}
