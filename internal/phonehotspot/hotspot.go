// Package phonehotspot defines the system's built-in phone hotspot switch.
package phonehotspot

import (
	"context"
	"sync"
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
	State           string      `json:"state"`
	Reason          string      `json:"reason,omitempty"`
	Connection      *Connection `json:"connection,omitempty"`
	Settings        *Settings   `json:"settings,omitempty"`
	SettingsInvalid bool        `json:"settingsInvalid,omitempty"`
	Devices         []Device    `json:"devices,omitzero"`
}

type Device struct {
	MAC  string `json:"mac"`
	IP   string `json:"ip,omitempty"`
	Name string `json:"name,omitempty"`
}

// Connection is returned only to administrators while the AP is running.
type Connection struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
	URL      string `json:"url"`
	Portal   bool   `json:"portal,omitempty"`
}

type Switch interface {
	PhoneHotspot(context.Context) (State, error)
	SetPhoneHotspot(context.Context, bool) (State, error)
	SavePhoneHotspotSettings(context.Context, SettingsChange) (Settings, error)
}

type Request struct {
	Enabled *bool `json:"enabled"`
}

// Memory backs Fake mode, which never touches the machine's network.
type Memory struct {
	mu         sync.Mutex
	profile    Profile
	connection *Connection
}

func (m *Memory) snapshot() State {
	settings := m.profile.Settings()
	if m.connection != nil {
		devices := []Device{{MAC: "02:00:00:00:00:21", IP: "192.168.173.21", Name: "示例手机"}}
		return State{State: On, Connection: m.connection, Settings: &settings, Devices: devices}
	}
	return State{State: Off, Settings: &settings}
}

func (m *Memory) PhoneHotspot(context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot(), nil
}

func (m *Memory) SetPhoneHotspot(_ context.Context, enabled bool) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !enabled {
		m.connection = nil
	} else if m.connection == nil {
		ssid, password := m.profile.SSID, m.profile.Password
		if ssid == "" {
			ssid = "A-NAS-3EB129"
		}
		if password == "" {
			password = "preview-hotspot-only"
		}
		m.connection = &Connection{SSID: ssid, Password: password, URL: "https://hotspot.example.test:8443/", Portal: m.profile.Portal}
	}
	return m.snapshot(), nil
}

func (m *Memory) SavePhoneHotspotSettings(_ context.Context, change SettingsChange) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.profile.Apply(change); err != nil {
		return Settings{}, err
	}
	return m.profile.Settings(), nil
}
