package phonehotspot

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidSSID     = errors.New("invalid hotspot name")
	ErrInvalidPassword = errors.New("invalid hotspot password")
)

// Settings is the saved configuration summary. It never contains a password.
type Settings struct {
	SSID               string `json:"ssid"`
	PasswordConfigured bool   `json:"passwordConfigured"`
	Portal             bool   `json:"portal"`
}

// Omitted fields retain their current value. An empty password restores a
// fresh random password at each start; an empty SSID restores the device name.
type SettingsChange struct {
	SSID     *string `json:"ssid,omitempty"`
	Password *string `json:"password,omitempty"`
	Portal   *bool   `json:"portal,omitempty"`
}

// Profile is read by the hotspot service from the Host Agent's private state.
type Profile struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
	Portal   bool   `json:"portal"`
}

func (c SettingsChange) Validate() error {
	if c.SSID != nil {
		ssid := *c.SSID
		if len(ssid) > 32 || !utf8.ValidString(ssid) || strings.ContainsFunc(ssid, unicode.IsControl) ||
			(ssid != "" && strings.TrimSpace(ssid) == "") {
			return ErrInvalidSSID
		}
	}
	if c.Password != nil && *c.Password != "" {
		password := *c.Password
		if len(password) < 8 || len(password) > 63 ||
			strings.ContainsFunc(password, func(r rune) bool { return r < ' ' || r > '~' }) {
			return ErrInvalidPassword
		}
	}
	return nil
}

func (p *Profile) Apply(change SettingsChange) error {
	if err := change.Validate(); err != nil {
		return err
	}
	if change.SSID != nil {
		p.SSID = *change.SSID
	}
	if change.Password != nil {
		p.Password = *change.Password
	}
	if change.Portal != nil {
		p.Portal = *change.Portal
	}
	return nil
}

func (p Profile) Settings() Settings {
	return Settings{SSID: p.SSID, PasswordConfigured: p.Password != "", Portal: p.Portal}
}
