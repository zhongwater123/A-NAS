package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/phonehotspot"
)

const phoneHotspotUnit = "anas-phone-hotspot.service"

func (e *Executor) hotspotFile(parts ...string) string {
	return filepath.Join(append([]string{e.systemRoot}, parts...)...)
}

func (e *Executor) hasHotspotTools() (bool, error) {
	for _, name := range []string{"hostapd", "hostapd_cli", "dnsmasq", "iw", "nft", "ip"} {
		if _, err := os.Stat(e.hotspotFile("usr", "sbin", name)); errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, fmt.Errorf("inspect hotspot dependencies: %w", err)
		}
	}
	return true, nil
}

func (e *Executor) PhoneHotspot(ctx context.Context) (phonehotspot.State, error) {
	out, err := e.runner.Output(ctx, "systemctl", []string{"show", "--property=ActiveState", "--value", phoneHotspotUnit}, "")
	if err != nil {
		return phonehotspot.State{}, commandError("read the phone hotspot service", err, out)
	}
	switch strings.TrimSpace(string(out)) {
	case "activating":
		return phonehotspot.State{State: phonehotspot.Starting}, nil
	case "deactivating":
		return phonehotspot.State{State: phonehotspot.Stopping}, nil
	case "active":
		data, err := os.ReadFile(e.hotspotFile("run", "anas-phone-hotspot", "connection.json"))
		if errors.Is(err, os.ErrNotExist) { // The service can stop between the two reads.
			return phonehotspot.State{State: phonehotspot.Stopping}, nil
		}
		if err != nil {
			return phonehotspot.State{}, fmt.Errorf("read phone hotspot connection: %w", err)
		}
		var connection phonehotspot.Connection
		if json.Unmarshal(data, &connection) != nil || connection.SSID == "" || connection.Password == "" || connection.URL == "" {
			return phonehotspot.State{}, errors.New("invalid phone hotspot connection")
		}
		return phonehotspot.State{State: phonehotspot.On, Connection: &connection}, nil
	case "failed":
		reason, _ := os.ReadFile(e.hotspotFile("run", "anas-phone-hotspot", "reason"))
		switch code := strings.TrimSpace(string(reason)); code {
		case "dependencies_missing", "no_wired_uplink", "no_idle_radio", "subnet_conflict", "ap_failed", "dhcp_failed", "web_unavailable":
			return phonehotspot.State{State: phonehotspot.Failed, Reason: code}, nil
		}
		return phonehotspot.State{State: phonehotspot.Failed, Reason: "service_failed"}, nil
	default:
		installed, err := e.hasHotspotTools()
		if err != nil {
			return phonehotspot.State{}, err
		}
		if !installed {
			return phonehotspot.State{State: phonehotspot.Unavailable, Reason: "dependencies_missing"}, nil
		}
		return phonehotspot.State{State: phonehotspot.Off}, nil
	}
}

func (e *Executor) SetPhoneHotspot(ctx context.Context, enabled bool) (phonehotspot.State, error) {
	action := "stop"
	state := phonehotspot.Stopping
	if enabled {
		installed, err := e.hasHotspotTools()
		if err != nil {
			return phonehotspot.State{}, err
		}
		if !installed {
			return phonehotspot.State{State: phonehotspot.Unavailable, Reason: "dependencies_missing"}, nil
		}
		action, state = "start", phonehotspot.Starting
	}
	// Submit even if the snapshot matches the target. systemd orders and
	// replaces pending jobs, including requests from different admin sessions.
	if out, err := e.runner.Run(ctx, "systemctl", []string{action, "--no-block", phoneHotspotUnit}, ""); err != nil {
		return phonehotspot.State{}, commandError(action+" the phone hotspot service", err, out)
	}
	return phonehotspot.State{State: state}, nil
}

var _ phonehotspot.Switch = (*Executor)(nil)
