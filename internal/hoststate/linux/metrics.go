package linux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

const (
	// baselineWindow is sampled when no recent baseline exists, so the first
	// answer is a real rate instead of zero.
	baselineWindow = 500 * time.Millisecond
	// minSampleInterval returns the cached result to callers polling faster
	// than counters can meaningfully change.
	minSampleInterval = 500 * time.Millisecond
	maxBaselineAge    = 15 * time.Second
)

type metricsSampler struct {
	mu       sync.Mutex
	previous *counterSample
	cached   hoststate.Metrics
}

type counterSample struct {
	at           time.Time
	cpuBusy      uint64
	cpuTotal     uint64
	logicalCores int
	interfaces   map[string]interfaceCounters
}

type interfaceCounters struct {
	received    uint64
	transmitted uint64
}

// ReadMetrics returns CPU and network rates since the previous call together
// with current memory use.
func (r *Reader) ReadMetrics(ctx context.Context) (hoststate.Metrics, error) {
	sampler := &r.metrics
	sampler.mu.Lock()
	defer sampler.mu.Unlock()

	current, err := r.sampleCounters()
	if err != nil {
		return hoststate.Metrics{}, err
	}
	previous := sampler.previous
	switch {
	case previous == nil || current.at.Sub(previous.at) > maxBaselineAge || !current.at.After(previous.at):
		if err := r.dependencies.sleep(ctx, baselineWindow); err != nil {
			return hoststate.Metrics{}, err
		}
		previous = current
		if current, err = r.sampleCounters(); err != nil {
			return hoststate.Metrics{}, err
		}
	case current.at.Sub(previous.at) < minSampleInterval:
		return sampler.cached, nil
	}

	memory, err := readMemory(r.dependencies.root)
	if err != nil {
		return hoststate.Metrics{}, fmt.Errorf("read memory: %w", err)
	}
	seconds := current.at.Sub(previous.at).Seconds()
	if seconds <= 0 {
		return hoststate.Metrics{}, errors.New("metrics clock did not advance")
	}
	received, transmitted := networkDeltas(previous.interfaces, current.interfaces)
	metrics := hoststate.Metrics{
		ObservedAt: current.at.UTC(),
		CPU: hoststate.CPUMetrics{
			UsagePercent: cpuUsagePercent(previous, current),
			LogicalCores: current.logicalCores,
		},
		Memory: memory,
		Network: hoststate.NetworkMetrics{
			ReceiveBytesPerSecond:  uint64(math.Round(float64(received) / seconds)),
			TransmitBytesPerSecond: uint64(math.Round(float64(transmitted) / seconds)),
		},
	}
	if !metrics.Valid() {
		return hoststate.Metrics{}, errors.New("metrics observation is inconsistent")
	}
	sampler.previous = current
	sampler.cached = metrics
	return metrics, nil
}

func (r *Reader) sampleCounters() (*counterSample, error) {
	busy, total, cores, err := readCPU(r.dependencies.root)
	if err != nil {
		return nil, fmt.Errorf("read cpu: %w", err)
	}
	interfaces, err := readNetwork(r.dependencies.root)
	if err != nil {
		return nil, fmt.Errorf("read network: %w", err)
	}
	return &counterSample{at: r.dependencies.now(), cpuBusy: busy, cpuTotal: total, logicalCores: cores, interfaces: interfaces}, nil
}

func cpuUsagePercent(previous, current *counterSample) float64 {
	total := counterDelta(previous.cpuTotal, current.cpuTotal)
	if total == 0 {
		return 0
	}
	busy := min(counterDelta(previous.cpuBusy, current.cpuBusy), total)
	return math.Round(float64(busy)/float64(total)*1000) / 10
}

// networkDeltas only counts interfaces present in both samples, so a link
// appearing with large lifetime counters does not register as a burst.
func networkDeltas(previous, current map[string]interfaceCounters) (received, transmitted uint64) {
	for name, now := range current {
		before, ok := previous[name]
		if !ok {
			continue
		}
		received += counterDelta(before.received, now.received)
		transmitted += counterDelta(before.transmitted, now.transmitted)
	}
	return received, transmitted
}

// counterDelta treats a decreasing counter as a reset rather than a wrap.
func counterDelta(before, after uint64) uint64 {
	if after < before {
		return 0
	}
	return after - before
}

// readCPU parses the aggregate line of /proc/stat. Guest time is already
// included in user time, so only the first eight fields form the total.
func readCPU(root fs.FS) (busy, total uint64, cores int, err error) {
	contents, err := fs.ReadFile(root, "proc/stat")
	if err != nil {
		return 0, 0, 0, err
	}
	found := false
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			cores++
			continue
		}
		if len(fields) < 9 {
			return 0, 0, 0, errors.New("aggregate cpu line is incomplete")
		}
		var values [8]uint64
		for i := range values {
			if values[i], err = strconv.ParseUint(fields[i+1], 10, 64); err != nil {
				return 0, 0, 0, errors.New("aggregate cpu line is invalid")
			}
			total += values[i]
		}
		idle := values[3] + values[4]
		busy = total - idle
		found = true
	}
	if !found || cores == 0 {
		return 0, 0, 0, errors.New("cpu statistics are missing")
	}
	return busy, total, cores, nil
}

func readMemory(root fs.FS) (hoststate.MemoryMetrics, error) {
	contents, err := fs.ReadFile(root, "proc/meminfo")
	if err != nil {
		return hoststate.MemoryMetrics{}, err
	}
	values := make(map[string]uint64, 2)
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		kibibytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return hoststate.MemoryMetrics{}, fmt.Errorf("%s is invalid", strings.TrimSuffix(fields[0], ":"))
		}
		values[fields[0]] = kibibytes * 1024
	}
	total, hasTotal := values["MemTotal:"]
	available, hasAvailable := values["MemAvailable:"]
	if !hasTotal || !hasAvailable || total == 0 {
		return hoststate.MemoryMetrics{}, errors.New("MemTotal or MemAvailable is missing")
	}
	return hoststate.MemoryMetrics{TotalBytes: total, UsedBytes: total - min(available, total)}, nil
}

// readNetwork returns counters for physical interfaces (those backed by a
// device in sysfs). Hosts without any, such as some VMs, fall back to every
// non-loopback interface.
func readNetwork(root fs.FS) (map[string]interfaceCounters, error) {
	contents, err := fs.ReadFile(root, "proc/net/dev")
	if err != nil {
		return nil, err
	}
	all := make(map[string]interfaceCounters)
	physical := make(map[string]interfaceCounters)
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		name, rest, ok := strings.Cut(scanner.Text(), ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || name == "lo" || strings.ContainsAny(name, "/|") {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			return nil, fmt.Errorf("interface %s counters are incomplete", name)
		}
		received, errReceived := strconv.ParseUint(fields[0], 10, 64)
		transmitted, errTransmitted := strconv.ParseUint(fields[8], 10, 64)
		if errReceived != nil || errTransmitted != nil {
			return nil, fmt.Errorf("interface %s counters are invalid", name)
		}
		counters := interfaceCounters{received: received, transmitted: transmitted}
		all[name] = counters
		if _, err := fs.Stat(root, "sys/class/net/"+name+"/device"); err == nil {
			physical[name] = counters
		}
	}
	if len(physical) > 0 {
		return physical, nil
	}
	return all, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
