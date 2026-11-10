package linux

import (
	"context"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

// Super I/O drivers expose hundreds of attributes in one directory. MapFS
// sorts names, so these unrelated attributes deterministically precede inputs.
func TestThermalInputsAfterHundredsOfAttributes(t *testing.T) {
	r, root, _ := taskFixture(t)
	const directory = "sys/class/hwmon/hwmon3"
	sensorFiles(root, directory, map[string]string{
		"name": "cpu_thermal", "fan2_input": "2054",
		"temp2_input": "62000", "temp2_crit": "100000",
	})
	for i := range 364 {
		root[fmt.Sprintf("%s/attribute%03d", directory, i)] = &fstest.MapFile{}
	}
	got, err := r.ReadDeviceStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Fans.Status != taskmanager.Available || len(got.Fans.Items) != 1 || got.Fans.Items[0].Number != 2 || got.Fans.Items[0].RPM != 2054 {
		t.Errorf("fans = %+v, want fan 2 at 2054 RPM", got.Fans)
	}
	if got.CPU.Temperature == nil || got.CPU.Temperature.Celsius != 62 || got.CPU.Temperature.HotCelsius != 90 {
		t.Errorf("temperature = %+v, want 62 C hot from 90 C", got.CPU.Temperature)
	}
	root[directory+"/fan2_input"].Data = []byte("0")
	fans, found := r.fans()
	if !found || len(fans) != 1 || !fans[0].Stopped {
		t.Errorf("fans after stopping = %+v, found %v", fans, found)
	}
}

func TestSMARTTemperatureRange(t *testing.T) {
	for _, field := range []string{"temperature", "nvme_smart_health_information_log"} {
		for _, value := range []int{-128, -41, -40, 0, 41, 150, 151, 255} {
			t.Run(fmt.Sprintf("%s/%d", field, value), func(t *testing.T) {
				key := "current"
				if field != "temperature" {
					key = "temperature"
				}
				output := fmt.Sprintf(`{"smart_status":{"passed":false},%q:{%q:%d}}`, field, key, value)
				reading, asleep := parseSMART([]byte(output))
				if asleep || reading.health != hoststate.HealthCritical {
					t.Fatalf("temperature must not change SMART health: %+v, asleep %v", reading, asleep)
				}
				if value < -40 || value > 150 {
					if reading.temperature != nil {
						t.Fatalf("temperature = %d, want missing", *reading.temperature)
					}
				} else if reading.temperature == nil || *reading.temperature != value {
					t.Fatalf("temperature = %v, want %d", reading.temperature, value)
				}
			})
		}
	}
}

func TestInvalidSMARTTemperatureKeepsDeviceSnapshotAvailable(t *testing.T) {
	r, root, _ := taskFixture(t)
	r.dependencies.readBlockDevices = func(context.Context) ([]byte, error) {
		return []byte(`{"blockdevices":[
			{"name":"sda","type":"disk","size":512000000000,"model":"Bad sensor","tran":"sata","rota":true,"wwn":"bad"},
			{"name":"sdb","type":"disk","size":512000000000,"model":"Good sensor","tran":"sata","rota":true,"wwn":"good","mountpoints":["/"]}
		]}`), nil
	}
	r.dependencies.readSMART = func(_ context.Context, name string) ([]byte, error) {
		if name == "sda" {
			return []byte(`{"smart_status":{"passed":false},"temperature":{"current":255}}`), nil
		}
		return []byte(healthySMART), nil
	}
	for _, name := range []string{"sda", "sdb"} {
		root["sys/class/block/"+name+"/stat"] = &fstest.MapFile{Data: []byte("1 0 8 0 1 0 8 0 0 0 0\n")}
	}
	if err := r.RefreshSMART(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadDeviceStatus(context.Background())
	if err != nil || !got.Valid() {
		t.Fatalf("device snapshot must remain valid: %+v, error %v", got, err)
	}
	if got.CPU.UsagePercent == nil || got.Memory.UsedBytes == 0 || len(got.Network.Items) != 1 || len(got.Disks.Items) != 2 {
		t.Fatalf("other devices must remain visible: %+v", got)
	}
	for _, disk := range got.Disks.Items {
		if disk.Model == "Bad sensor" && disk.Temperature != nil {
			t.Errorf("invalid temperature remains: %+v", disk)
		}
		if disk.Model == "Good sensor" && (disk.Temperature == nil || disk.Temperature.Celsius != 41) {
			t.Errorf("valid temperature lost: %+v", disk)
		}
	}
}
