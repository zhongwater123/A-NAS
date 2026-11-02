package linux

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// SMART readings are taken apart from the state reads that show them. One
// smartctl run on a busy hard disk takes about a second, and every page of
// the desktop and every file request used to wait for one per disk; when
// the caller gave up, smartctl was killed with the request.
const (
	// smartInterval is how long readings are reused before a state read
	// starts a background refresh.
	smartInterval = 10 * time.Minute
	// smartTimeout bounds smartctl for one disk.
	smartTimeout = 30 * time.Second
	// smartMaxAge returns a reading nobody could renew, such as that of a
	// disk asleep for a day, to unknown.
	smartMaxAge = 24 * time.Hour
)

type smartCache struct {
	mu         sync.Mutex
	readings   map[string]smartReading // by stable disk identity
	refreshed  time.Time
	refreshing bool
}

type smartReading struct {
	health      hoststate.Health
	temperature *int
	at          time.Time
}

// smartTarget is a disk SMART can be read from: its stable identity and its
// current kernel name.
type smartTarget struct {
	identity string
	name     string
}

// readSMARTCommand reads health and temperature only, not the error and
// self-test logs, and leaves a disk in standby asleep: smartctl then exits
// with bit 1 and says so instead of spinning the disk up.
func readSMARTCommand(ctx context.Context, deviceName string) ([]byte, error) {
	if deviceName == "" || strings.ContainsAny(deviceName, `/\\`) {
		return nil, errors.New("invalid block device name")
	}
	output, err := exec.CommandContext(ctx, "smartctl", "--json", "--nocheck=standby", "--health", "--attributes", "/dev/"+deviceName).Output()
	if err != nil && len(output) == 0 {
		return nil, err
	}
	return output, nil
}

// smartFor returns the reading of a disk, or unknown when there is none or
// it is too old.
func (r *Reader) smartFor(identity string) (hoststate.Health, *int) {
	cache := &r.smart
	cache.mu.Lock()
	defer cache.mu.Unlock()
	reading, ok := cache.readings[identity]
	if !ok || r.dependencies.now().Sub(reading.at) > smartMaxAge {
		return hoststate.HealthUnknown, nil
	}
	return reading.health, reading.temperature
}

// diskReadings returns a disk's SMART verdict and temperature. An NVMe
// drive's temperature is read now from its own sensor, that of any other
// disk is its last SMART reading.
func (r *Reader) diskReadings(identity string, device blockDevice) (hoststate.Health, *int) {
	health, celsius := r.smartFor(identity)
	if strings.EqualFold(device.Transport, "nvme") && device.Name != "" && !strings.ContainsAny(device.Name, `/\\`) {
		if live := readDeviceTemperature(r.dependencies.root, "sys/class/block/"+device.Name+"/device", "Composite"); live != nil {
			celsius = &live.Celsius
		}
	}
	return health, celsius
}

// RefreshSMART reads every disk's SMART now and waits for it. The Host Agent
// calls it once at startup so the first state read already has readings.
func (r *Reader) RefreshSMART(ctx context.Context) error {
	if !r.startSMARTRefresh(true) {
		return nil
	}
	contents, err := r.dependencies.readBlockDevices(ctx)
	if err != nil {
		r.finishSMARTRefresh(nil, false)
		return err
	}
	_, targets, err := parseDisks(contents, nil, nil)
	if err != nil {
		r.finishSMARTRefresh(nil, false)
		return err
	}
	r.refreshSMART(ctx, targets)
	return nil
}

// refreshSMARTInBackground starts a refresh when the readings are older
// than smartInterval and none is running. It never waits for smartctl and
// does not share the caller's context.
func (r *Reader) refreshSMARTInBackground(targets []smartTarget) {
	if !r.startSMARTRefresh(false) {
		return
	}
	go r.refreshSMART(context.Background(), targets)
}

func (r *Reader) startSMARTRefresh(force bool) bool {
	if r.dependencies.readSMART == nil {
		return false
	}
	cache := &r.smart
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.refreshing {
		return false
	}
	if !force && !cache.refreshed.IsZero() && r.dependencies.now().Sub(cache.refreshed) < smartInterval {
		return false
	}
	cache.refreshing = true
	return true
}

func (r *Reader) refreshSMART(ctx context.Context, targets []smartTarget) {
	cache := &r.smart
	cache.mu.Lock()
	previous := cache.readings
	cache.mu.Unlock()

	readings := make(map[string]smartReading, len(targets))
	for _, target := range targets {
		readCtx, cancel := context.WithTimeout(ctx, smartTimeout)
		output, err := r.dependencies.readSMART(readCtx, target.name)
		cancel()
		if err != nil {
			readings[target.identity] = smartReading{health: hoststate.HealthUnknown, at: r.dependencies.now()}
			continue
		}
		reading, asleep := parseSMART(output)
		if asleep {
			if last, ok := previous[target.identity]; ok {
				readings[target.identity] = last
			}
			continue
		}
		reading.at = r.dependencies.now()
		readings[target.identity] = reading
	}
	r.finishSMARTRefresh(readings, true)
}

func (r *Reader) finishSMARTRefresh(readings map[string]smartReading, refreshed bool) {
	cache := &r.smart
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if refreshed {
		cache.readings = readings
		cache.refreshed = r.dependencies.now()
	}
	cache.refreshing = false
}

type smartDocument struct {
	Smartctl *struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String string `json:"string"`
		} `json:"messages"`
	} `json:"smartctl"`
	SMARTStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	NVMeHealth *struct {
		Temperature int `json:"temperature"`
	} `json:"nvme_smart_health_information_log"`
}

// parseSMART reads health and temperature from smartctl's JSON, and whether
// smartctl skipped the disk because it was asleep. That is exit bit 1 with
// smartctl's "Device is in STANDBY mode" message, as Netdata's smartctl
// collector tells it apart from a device that failed to open.
func parseSMART(contents []byte) (smartReading, bool) {
	reading := smartReading{health: hoststate.HealthUnknown}
	var document smartDocument
	if len(contents) == 0 || json.Unmarshal(contents, &document) != nil {
		return reading, false
	}
	if document.Smartctl != nil && document.Smartctl.ExitStatus&(1<<1) != 0 {
		for _, message := range document.Smartctl.Messages {
			if strings.HasPrefix(message.String, "Device is in") && strings.Contains(message.String, "mode") {
				return reading, true
			}
		}
	}
	if document.SMARTStatus != nil {
		if document.SMARTStatus.Passed {
			reading.health = hoststate.HealthHealthy
		} else {
			reading.health = hoststate.HealthCritical
		}
	}
	if document.Temperature != nil {
		value := document.Temperature.Current
		reading.temperature = &value
	} else if document.NVMeHealth != nil {
		value := document.NVMeHealth.Temperature
		reading.temperature = &value
	}
	return reading, false
}
