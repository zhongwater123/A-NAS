package textroute_test

import (
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos/textroute"
)

func TestTheBuiltInRouteIsCalibrated(t *testing.T) {
	route, err := textroute.V1()
	if err != nil {
		t.Fatal(err)
	}
	if route.Model == "" || len(route.Queries) == 0 || route.Threshold <= 0 || route.Threshold >= 1 {
		t.Fatalf("V1() = %+v", route)
	}
}

func TestARouteNeedsAModelQueriesAndAThreshold(t *testing.T) {
	for _, data := range []string{
		`{"queries": ["收据"], "threshold": 0.7}`,
		`{"model": "m", "queries": [], "threshold": 0.7}`,
		`{"model": "m", "queries": ["收据", "收据"], "threshold": 0.7}`,
		`{"model": "m", "queries": [""], "threshold": 0.7}`,
		`{"model": "m", "queries": ["收据"], "threshold": 1}`,
		`not json`,
	} {
		if _, err := textroute.Parse([]byte(data)); err == nil {
			t.Errorf("Parse(%s) succeeded", data)
		}
	}
}
