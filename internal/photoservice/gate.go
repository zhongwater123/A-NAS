package photoservice

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/prometheus/procfs"
)

// Gate lets background AI work start only while nobody is using the photo
// library and the system is not under pressure (docs/architecture/photo-ai.md).
// Activity elsewhere on the device, such as Web files and SMB transfers,
// reaches it as CPU and I/O pressure until the Host Agent reports it directly.
// Work already started is allowed to finish.
type Gate struct {
	quiet   time.Duration
	now     func() time.Time
	lastUse atomic.Int64
	// pressure returns the share of the last 10 seconds in which some task
	// stalled on a resource, in percent.
	pressure func(resource string) (float64, error)
}

// pressureLimits are starting values, to be tuned on the device.
var pressureLimits = []struct {
	resource string
	limit    float64
}{{"cpu", 20}, {"io", 10}, {"memory", 5}}

// QuietPeriod is how long the photo library must go unused before AI work
// starts.
const QuietPeriod = 5 * time.Minute

// NewGate returns a gate that stays closed for quiet after any use, starting
// now.
func NewGate(quiet time.Duration) *Gate {
	gate := &Gate{quiet: quiet, now: time.Now, pressure: someAvg10}
	gate.Touch()
	return gate
}

// Touch records that someone is using the photo library.
func (g *Gate) Touch() { g.lastUse.Store(g.now().UnixNano()) }

func (g *Gate) Open(context.Context) (bool, string) {
	if g.now().Sub(time.Unix(0, g.lastUse.Load())) < g.quiet {
		return false, "foreground"
	}
	for _, check := range pressureLimits {
		value, err := g.pressure(check.resource)
		if err != nil {
			// A kernel without pressure stall information leaves only
			// the activity check.
			continue
		}
		if value > check.limit {
			return false, check.resource + "_pressure"
		}
	}
	return true, ""
}

func someAvg10(resource string) (float64, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return 0, err
	}
	stats, err := fs.PSIStatsForResource(resource)
	if err != nil {
		return 0, err
	}
	if stats.Some == nil {
		return 0, errors.New("no pressure information for " + resource)
	}
	return stats.Some.Avg10, nil
}
