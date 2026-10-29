package httpapi

import (
	"net/http"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
)

func TestInvalidPostureCannotRegisterARoute(t *testing.T) {
	for _, posture := range []apiroute.Posture{apiroute.Unset, -1, apiroute.Admin + 1} {
		for _, method := range []string{"", http.MethodGet, http.MethodPost} {
			route := apiroute.Route{Method: method, Pattern: "/test", Posture: posture}
			if err := checkPosture(route); err == nil {
				t.Errorf("accepted %s with posture %d", method, posture)
			}
		}
	}
}
