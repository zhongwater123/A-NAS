// Package phonehotspot defines the system's built-in phone hotspot switch.
package phonehotspot

import (
	"context"
	"sync/atomic"
)

const (
	Unavailable = "unavailable"
	Off         = "off"
	Starting    = "starting"
	On          = "on"
	Stopping    = "stopping"
	Failed      = "failed"
)

type State struct {
	State      string      `json:"state"`
	Reason     string      `json:"reason,omitempty"`
	Connection *Connection `json:"connection,omitempty"`
}

// Connection is returned only to administrators while the AP is running.
type Connection struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
	URL      string `json:"url"`
}

type Switch interface {
	PhoneHotspot(context.Context) (State, error)
	SetPhoneHotspot(context.Context, bool) (State, error)
}

type Request struct {
	Enabled *bool `json:"enabled"`
}

// Memory backs Fake mode, which never touches the machine's network.
type Memory struct{ enabled atomic.Bool }

func (m *Memory) PhoneHotspot(context.Context) (State, error) {
	if m.enabled.Load() {
		return State{State: On, Connection: &Connection{
			SSID: "A-NAS-3EB129", Password: "preview-hotspot-only", URL: "http://192.168.173.1/",
		}}, nil
	}
	return State{State: Off}, nil
}

func (m *Memory) SetPhoneHotspot(ctx context.Context, enabled bool) (State, error) {
	m.enabled.Store(enabled)
	return m.PhoneHotspot(ctx)
}
