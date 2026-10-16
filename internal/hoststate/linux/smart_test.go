package linux

import (
	"context"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

const (
	healthySMART = `{"smartctl":{"exit_status":0},"smart_status":{"passed":true},"temperature":{"current":41}}`
	asleepSMART  = `{"smartctl":{"exit_status":2,"messages":[{"string":"Device is in STANDBY mode, exit(2)","severity":"information"}]}}`
)

// smartClock is a test clock shared with the background refresh.
type smartClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *smartClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *smartClock) Advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

func newSMARTReader(clock *smartClock, readSMART func(context.Context, string) ([]byte, error)) *Reader {
	return newReader(dependencies{
		root: fstest.MapFS{
			"etc/os-release": &fstest.MapFile{Data: []byte("ID=debian\nNAME=Debian\nVERSION_ID=13\n")},
			"etc/machine-id": &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
			"proc/uptime":    &fstest.MapFile{Data: []byte("1.0 2.0\n")},
		},
		hostname:     func() (string, error) { return "a-nas-dev", nil },
		architecture: "amd64",
		now:          clock.Now,
		readBlockDevices: func(context.Context) ([]byte, error) {
			return []byte(`{"blockdevices":[
              {"name":"nvme0n1","type":"disk","size":125000000000,"model":"System","tran":"nvme","wwn":"system",
               "children":[{"name":"nvme0n1p2","type":"part","mountpoints":["/"]}]},
              {"name":"sda","type":"disk","size":500107862016,"model":"ST500DM002-1BD142","tran":"sata","rota":true,"wwn":"data",
               "children":[{"name":"sda1","type":"part","fstype":"btrfs","mountpoints":["/srv/a-nas/data"]}]}
            ]}`), nil
		},
		readSMART: readSMART,
	})
}

func dataDisk(t *testing.T, state hoststate.State) hoststate.Disk {
	t.Helper()
	for _, disk := range state.Disks {
		if disk.Role == hoststate.DiskRoleData {
			return disk
		}
	}
	t.Fatal("no data disk")
	return hoststate.Disk{}
}

func waitForSMARTRefresh(t *testing.T, reader *Reader) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		reader.smart.mu.Lock()
		done := !reader.smart.refreshing
		reader.smart.mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("SMART refresh did not finish")
}

// A slow hard disk used to hold every state read for its smartctl run, and a
// caller that gave up killed smartctl.
func TestStateReadsDoNotWaitForSMART(t *testing.T) {
	clock := &smartClock{now: time.Date(2026, time.October, 15, 10, 0, 0, 0, time.UTC)}
	release := make(chan struct{})
	canceled := make(chan string, 4)
	reader := newSMARTReader(clock, func(ctx context.Context, name string) ([]byte, error) {
		select {
		case <-release:
		case <-ctx.Done():
			canceled <- name
			return nil, ctx.Err()
		}
		return []byte(healthySMART), nil
	})

	request, cancelRequest := context.WithCancel(context.Background())
	started := time.Now()
	state, err := reader.Read(request)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Read() waited %v for SMART", elapsed)
	}
	if got := dataDisk(t, state).SMARTStatus; got != hoststate.HealthUnknown {
		t.Fatalf("SMART before any reading = %q, want unknown", got)
	}
	cancelRequest()
	time.Sleep(20 * time.Millisecond)
	select {
	case name := <-canceled:
		t.Fatalf("canceling the request canceled smartctl for %s", name)
	default:
	}

	close(release)
	waitForSMARTRefresh(t, reader)
	state, err = reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	disk := dataDisk(t, state)
	if disk.SMARTStatus != hoststate.HealthHealthy || disk.TemperatureCelsius == nil || *disk.TemperatureCelsius != 41 {
		t.Fatalf("data disk = %+v, want healthy at 41 °C", disk)
	}
}

func TestSMARTIsReadAtMostOnceAnInterval(t *testing.T) {
	clock := &smartClock{now: time.Date(2026, time.October, 15, 10, 0, 0, 0, time.UTC)}
	var mu sync.Mutex
	reads := map[string]int{}
	reader := newSMARTReader(clock, func(_ context.Context, name string) ([]byte, error) {
		mu.Lock()
		reads[name]++
		mu.Unlock()
		return []byte(healthySMART), nil
	})
	count := func(name string) int {
		mu.Lock()
		defer mu.Unlock()
		return reads[name]
	}

	if err := reader.RefreshSMART(context.Background()); err != nil {
		t.Fatalf("RefreshSMART() error = %v", err)
	}
	for range 5 {
		if _, err := reader.Read(context.Background()); err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		clock.Advance(time.Minute)
	}
	waitForSMARTRefresh(t, reader)
	if got := count("sda"); got != 1 {
		t.Fatalf("smartctl runs for sda within the interval = %d, want 1", got)
	}

	clock.Advance(smartInterval)
	if _, err := reader.Read(context.Background()); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	waitForSMARTRefresh(t, reader)
	if got := count("sda"); got != 2 {
		t.Fatalf("smartctl runs for sda after the interval = %d, want 2", got)
	}
}

// smartctl --nocheck=standby leaves a sleeping disk asleep; its last reading
// stays until it is a day old.
func TestSleepingDiskKeepsItsLastReadingForADay(t *testing.T) {
	clock := &smartClock{now: time.Date(2026, time.October, 15, 10, 0, 0, 0, time.UTC)}
	var mu sync.Mutex
	asleep := false
	reader := newSMARTReader(clock, func(context.Context, string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if asleep {
			return []byte(asleepSMART), nil
		}
		return []byte(healthySMART), nil
	})
	if err := reader.RefreshSMART(context.Background()); err != nil {
		t.Fatalf("RefreshSMART() error = %v", err)
	}
	mu.Lock()
	asleep = true
	mu.Unlock()

	clock.Advance(12 * time.Hour)
	if err := reader.RefreshSMART(context.Background()); err != nil {
		t.Fatalf("RefreshSMART() error = %v", err)
	}
	state, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := dataDisk(t, state).SMARTStatus; got != hoststate.HealthHealthy {
		t.Fatalf("SMART of a sleeping disk = %q, want its last reading", got)
	}

	clock.Advance(13 * time.Hour)
	if err := reader.RefreshSMART(context.Background()); err != nil {
		t.Fatalf("RefreshSMART() error = %v", err)
	}
	state, err = reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	disk := dataDisk(t, state)
	if disk.SMARTStatus != hoststate.HealthUnknown || disk.TemperatureCelsius != nil {
		t.Fatalf("SMART after a day asleep = %+v, want unknown", disk)
	}
}

func TestParseSMARTTellsASleepingDiskFromAFailedOpen(t *testing.T) {
	for _, test := range []struct {
		name       string
		output     string
		wantAsleep bool
		wantHealth hoststate.Health
	}{
		{"standby", asleepSMART, true, hoststate.HealthUnknown},
		{"open failed", `{"smartctl":{"exit_status":2,"messages":[{"string":"Smartctl open device: /dev/sdb failed: No such device","severity":"error"}]}}`, false, hoststate.HealthUnknown},
		{"failing disk", `{"smartctl":{"exit_status":8},"smart_status":{"passed":false}}`, false, hoststate.HealthCritical},
		{"nvme", `{"smartctl":{"exit_status":0},"smart_status":{"passed":true},"nvme_smart_health_information_log":{"temperature":38}}`, false, hoststate.HealthHealthy},
	} {
		t.Run(test.name, func(t *testing.T) {
			reading, asleep := parseSMART([]byte(test.output))
			if asleep != test.wantAsleep || reading.health != test.wantHealth {
				t.Fatalf("parseSMART() = %+v, asleep %v; want %q, asleep %v", reading, asleep, test.wantHealth, test.wantAsleep)
			}
		})
	}
}
