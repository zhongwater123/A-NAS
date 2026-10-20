package photoservice

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/procfs"
)

// Gate lets background AI work start only while nobody is using the photo
// library and the device's other work is not held up (docs/architecture/photo-ai.md).
// Activity elsewhere on the device, such as Web files and SMB transfers,
// reaches it as CPU and I/O pressure until the Host Agent reports it directly.
// A running detection is stopped when the gate closes; other work already
// started is allowed to finish.
type Gate struct {
	quiet   time.Duration
	now     func() time.Time
	lastUse atomic.Int64
	// using counts requests still being served, such as a video playing or
	// a long upload; while there are any, the library is in use.
	using atomic.Int64
	// pressure returns the share of the last 10 seconds in which some task
	// other than the AI Worker's stalled on a resource, in percent.
	pressure func(resource string) (float64, error)
	// available returns the memory the system could still give out, in bytes.
	available func() (uint64, error)

	// watched lists the cgroups whose pressure counts; none means the whole
	// system's.
	watched []string
	// logger, when set, records each time pressure or short memory starts
	// and stops holding AI work back, as evidence for tuning the limits.
	logger *slog.Logger

	mu sync.Mutex
	// pressed holds the resources over their limit at the last look, and
	// short whether memory was below the reserve.
	pressed map[string]bool
	short   bool
}

// MemoryReserve is the memory AI work leaves for the user: below it the gate
// closes and the AI Worker is asked to release its models
// (docs/architecture/photo-objects.md#资源策略). A starting value, to be
// tuned on the device.
const MemoryReserve = 1536 << 20

// pressureLimits are how long, in percent of the last 10 seconds, the
// device's other work may wait on a resource before AI work stops; it starts
// again once the wait is below half of that. The AI Worker's own waiting does
// not count, so it may keep every core it is given busy. Starting values, to
// be tuned on the device (docs/architecture/photo-objects.md#资源策略).
var pressureLimits = []struct {
	resource string
	limit    float64
}{{"cpu", 10}, {"io", 10}, {"memory", 5}}

// QuietPeriod is how long the photo library must go unused before AI work
// starts.
const QuietPeriod = 5 * time.Minute

// NewGate returns a gate that stays closed for quiet after any use, starting
// now. A nil logger records nothing.
func NewGate(quiet time.Duration, logger *slog.Logger) *Gate {
	watched := systemSlices("/proc/self/cgroup", "/sys/fs/cgroup")
	gate := &Gate{quiet: quiet, now: time.Now, pressure: othersPressure(watched), available: memAvailable, watched: watched, logger: logger}
	gate.Touch()
	return gate
}

// Touch records that someone is using the photo library.
func (g *Gate) Touch() { g.lastUse.Store(g.now().UnixNano()) }

// Use records that someone uses the photo library until done is called;
// the quiet period starts then.
func (g *Gate) Use() (done func()) {
	g.using.Add(1)
	g.Touch()
	return func() {
		g.Touch()
		g.using.Add(-1)
	}
}

func (g *Gate) Open(context.Context) (bool, string) {
	if g.using.Load() > 0 || g.now().Sub(time.Unix(0, g.lastUse.Load())) < g.quiet {
		return false, "foreground"
	}
	for _, check := range pressureLimits {
		value, err := g.pressure(check.resource)
		if err != nil {
			// A kernel without pressure stall information leaves only
			// the activity check.
			continue
		}
		if g.over(check.resource, value, check.limit) {
			return false, check.resource + "_pressure"
		}
	}
	if g.available != nil {
		if free, err := g.available(); err == nil && g.memoryShort(free) {
			return false, "memory_low"
		}
	}
	return true, ""
}

// memoryShort reports whether free memory is below the reserve.
func (g *Gate) memoryShort(free uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	short := free < MemoryReserve
	if short != g.short && g.logger != nil {
		if short {
			g.logger.Info("AI work yields to low memory", "available_mib", free>>20, "reserve_mib", MemoryReserve>>20)
		} else {
			g.logger.Info("AI work no longer yields to low memory", "available_mib", free>>20)
		}
	}
	g.short = short
	return short
}

// over reports whether a resource is under pressure. Once over its limit it
// stays over until it falls below half of it, so a wait hovering at the limit
// does not stop and start AI work every few seconds.
func (g *Gate) over(resource string, value, limit float64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pressed == nil {
		g.pressed = map[string]bool{}
	}
	was := g.pressed[resource]
	if was {
		limit /= 2
	}
	over := value > limit
	g.pressed[resource] = over
	if over != was && g.logger != nil {
		if over {
			attrs := []any{"resource", resource, "avg10", value, "limit", limit}
			for _, slice := range g.watched {
				attrs = append(attrs, filepath.Base(slice), waiting(slice, resource))
			}
			g.logger.Info("AI work yields to pressure", attrs...)
		} else {
			g.logger.Info("AI work no longer yields to pressure", "resource", resource, "avg10", value, "below", limit)
		}
	}
	return over
}

// waiting describes the pressure of a slice and of the three of its members
// that waited most, such as "12.30: anas-host-agent.service 8.10,
// anas-photos.service 4.00", to tell which work held AI back.
func waiting(slice, resource string) string {
	own, err := cgroupSomeAvg10(filepath.Join(slice, resource+".pressure"))
	if err != nil {
		return "unknown"
	}
	type member struct {
		name  string
		avg10 float64
	}
	var members []member
	entries, _ := os.ReadDir(slice)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		value, err := cgroupSomeAvg10(filepath.Join(slice, entry.Name(), resource+".pressure"))
		if err == nil && value > 0 {
			members = append(members, member{entry.Name(), value})
		}
	}
	slices.SortFunc(members, func(a, b member) int { return cmp.Compare(b.avg10, a.avg10) })
	text := strconv.FormatFloat(own, 'f', 2, 64)
	for i, m := range members[:min(3, len(members))] {
		separator := ", "
		if i == 0 {
			separator = ": "
		}
		text += separator + m.name + " " + strconv.FormatFloat(m.avg10, 'f', 2, 64)
	}
	return text
}

// systemSlices returns the cgroups that hold the device's work other than
// the AI Worker: system.slice and user.slice of the systemd instance this
// process runs under, read from its own cgroup. The Worker runs in
// anas-ai.slice, which systemd places under anas.slice, beside them. Outside a
// systemd service there are none.
func systemSlices(selfCgroup, mount string) []string {
	data, err := os.ReadFile(selfCgroup)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		i := strings.LastIndex(path, "/system.slice/")
		if i < 0 {
			return nil
		}
		var slices []string
		for _, name := range []string{"system.slice", "user.slice"} {
			slice := filepath.Join(mount, path[:i], name)
			if _, err := os.Stat(filepath.Join(slice, "cpu.pressure")); err == nil {
				slices = append(slices, slice)
			}
		}
		return slices
	}
	return nil
}

// othersPressure measures the highest pressure among slices, or the whole
// system's when there are none, which then includes the AI Worker's own.
func othersPressure(slices []string) func(resource string) (float64, error) {
	if len(slices) == 0 {
		return someAvg10
	}
	return func(resource string) (float64, error) {
		highest, found := 0.0, false
		var failed error
		for _, slice := range slices {
			value, err := cgroupSomeAvg10(filepath.Join(slice, resource+".pressure"))
			if err != nil {
				failed = err
				continue
			}
			highest, found = max(highest, value), true
		}
		if !found {
			return 0, failed
		}
		return highest, nil
	}
}

// cgroupSomeAvg10 reads the "some avg10" of a cgroup's pressure file, which
// has the format of /proc/pressure (procfs reads only the latter).
func cgroupSomeAvg10(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "some" {
			continue
		}
		for _, field := range fields[1:] {
			if value, ok := strings.CutPrefix(field, "avg10="); ok {
				return strconv.ParseFloat(value, 64)
			}
		}
	}
	return 0, errors.New("no some avg10 in " + path)
}

func memAvailable() (uint64, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return 0, err
	}
	info, err := fs.Meminfo()
	if err != nil {
		return 0, err
	}
	if info.MemAvailableBytes == nil {
		return 0, errors.New("no MemAvailable")
	}
	return *info.MemAvailableBytes, nil
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
