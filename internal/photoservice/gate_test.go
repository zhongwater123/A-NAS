package photoservice

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGateWaitsForQuietAndLowPressure(t *testing.T) {
	now := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	pressure := map[string]float64{"cpu": 2, "io": 1, "memory": 0}
	gate := &Gate{
		quiet: 5 * time.Minute,
		now:   func() time.Time { return now },
		pressure: func(resource string) (float64, error) {
			value, ok := pressure[resource]
			if !ok {
				return 0, errors.New("no pressure information")
			}
			return value, nil
		},
	}
	open := func() (bool, string) { return gate.Open(context.Background()) }

	gate.Touch()
	if ok, reason := open(); ok || reason != "foreground" {
		t.Fatalf("Open() right after use = %v, %q; want closed for foreground", ok, reason)
	}
	now = now.Add(5 * time.Minute)
	if ok, reason := open(); !ok {
		t.Fatalf("Open() after the quiet period = closed (%s)", reason)
	}

	pressure["io"] = 30
	if ok, reason := open(); ok || reason != "io_pressure" {
		t.Fatalf("Open() under I/O pressure = %v, %q", ok, reason)
	}
	// A kernel without pressure information leaves only the activity check.
	delete(pressure, "io")
	if ok, _ := open(); !ok {
		t.Fatal("Open() without I/O pressure information = closed")
	}
}
