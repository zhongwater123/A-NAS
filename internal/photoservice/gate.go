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
	// workerMemory returns the memory the AI Worker holds, in bytes: 0 while
	// it is not running. Without it the Worker counts as holding nothing.
	workerMemory func() uint64

	// watched lists the cgroups whose pressure counts; none means the whole
	// system's.
	watched []string
	// logger, when set, records each time pressure or short memory starts
	// and stops holding AI work back, as evidence for tuning the limits.
	logger *slog.Logger

	mu sync.Mutex
	// pressed holds the resources over their limit at the last look, short
	// whether memory was below the reserve, and waiting whether the models
	// waited for memory to load.
	pressed map[string]bool
	short   bool
	waiting bool
}

// MemoryReserve is the memory AI work always leaves for the user's next
// request (docs/architecture/photo-objects.md#资源策略): background work loads
// a model only if this much stays free afterwards, and below it the gate
// closes and the AI Worker is asked to release its models. The user set it
// to 1 GiB on 2026-10-28. Requests someone makes, such as a search, load what
// they need regardless.
const MemoryReserve = 1 << 30

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
	gate := &Gate{quiet: quiet, now: time.Now, pressure: othersPressure(watched), available: memAvailable,
		workerMemory: cgroupMemory(workerCgroup("/proc/self/cgroup", "/sys/fs/cgroup")), watched: watched, logger: logger}
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

// Open reports whether background AI work may start another item, and why
// not otherwise; need is the memory the AI Worker's models take, 0 for work
// that loads none (photos.Gate).
func (g *Gate) Open(_ context.Context, need uint64) (bool, string) {
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
		if free, err := g.available(); err == nil {
			if g.memoryShort(free) {
				return false, "memory_low"
			}
			// Work that loads no model, such as the vector sync, leaves the
			// models' waiting and its log alone.
			if need > 0 && g.cannotLoad(free, need) {
				return false, "memory_for_models"
			}
		}
	}
	return true, ""
}

// cannotLoad reports whether loading what the AI Worker does not hold yet of
// need would leave less than the reserve free. need and what the Worker
// holds are both counted as its cgroup counts memory, page cache included;
// the models' manifests round their measured growth up
// (docs/architecture/photo-objects.md#资源策略), so the estimate errs towards
// waiting.
func (g *Gate) cannotLoad(free, need uint64) bool {
	var held uint64
	if g.workerMemory != nil {
		held = g.workerMemory()
	}
	load := need - min(need, held)
	waiting := free < MemoryReserve+load
	g.mu.Lock()
	defer g.mu.Unlock()
	if waiting != g.waiting && g.logger != nil {
		if waiting {
			g.logger.Info("AI work waits for memory to load its models", "available_mib", free>>20,
				"load_mib", load>>20, "reserve_mib", MemoryReserve>>20)
		} else {
			g.logger.Info("AI work has memory to load its models", "available_mib", free>>20, "load_mib", load>>20)
		}
	}
	g.waiting = waiting
	return waiting
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
	var text strings.Builder
	text.WriteString(strconv.FormatFloat(own, 'f', 2, 64))
	for i, m := range members[:min(3, len(members))] {
		separator := ", "
		if i == 0 {
			separator = ": "
		}
		text.WriteString(separator + m.name + " " + strconv.FormatFloat(m.avg10, 'f', 2, 64))
	}
	return text.String()
}

// systemSlices returns the cgroups that hold the device's work other than
// the AI Worker: system.slice and user.slice of the systemd instance this
// process runs under, read from its own cgroup. The Worker runs in
// anas-ai.slice, which systemd places under anas.slice, beside them. Outside a
// systemd service there are none.
func systemSlices(selfCgroup, mount string) []string {
	root, ok := systemdRoot(selfCgroup)
	if !ok {
		return nil
	}
	var slices []string
	for _, name := range []string{"system.slice", "user.slice"} {
		slice := filepath.Join(mount, root, name)
		if _, err := os.Stat(filepath.Join(slice, "cpu.pressure")); err == nil {
			slices = append(slices, slice)
		}
	}
	return slices
}

// workerCgroup returns the AI Worker's cgroup in the systemd instance this
// process runs under (deploy/systemd/system/anas-ai.service), or "" outside a
// systemd service. It exists only while the Worker runs.
func workerCgroup(selfCgroup, mount string) string {
	root, ok := systemdRoot(selfCgroup)
	if !ok {
		return ""
	}
	return filepath.Join(mount, root, "anas.slice", "anas-ai.slice", "anas-ai.service")
}

// systemdRoot returns the cgroup of the systemd instance this process runs
// under as one of its services, read from its own cgroup: "" on the host, or
// a container's cgroup.
func systemdRoot(selfCgroup string) (string, bool) {
	data, err := os.ReadFile(selfCgroup)
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		root, _, ok := strings.CutLast(path, "/system.slice/")
		return root, ok
	}
	return "", false
}

// cgroupMemory returns a function reading the memory a cgroup holds, page
// cache included, or 0 while it does not exist; nil for no cgroup.
func cgroupMemory(cgroup string) func() uint64 {
	if cgroup == "" {
		return nil
	}
	return func() uint64 {
		data, err := os.ReadFile(filepath.Join(cgroup, "memory.current"))
		if err != nil {
			return 0
		}
		value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			return 0
		}
		return value
	}
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
	for line := range strings.SplitSeq(string(data), "\n") {
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
