package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
)

// Store keeps the service's files in its state directory, each replaced
// whole so that a crash leaves either the old file or the new one: the state
// the service leaves, the settings the administrator chooses, which the
// service only reads, and the request the Host Agent hands it. All are
// root's alone; the Host Agent, which runs as root, reads and writes them.
type Store struct {
	Dir string
}

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

// Load returns the state, a new one when there is none yet, with the
// settings from their own file.
func (s Store) Load() (State, error) {
	state := State{Phase: PhaseUpToDate}
	if err := s.read("state.json", &state); err != nil {
		return State{}, err
	}
	settings, err := s.Settings()
	if err != nil {
		return State{}, err
	}
	state.Settings = settings
	return state, nil
}

// Save replaces the state.
func (s Store) Save(state State) error { return s.write("state.json", state) }

// Settings returns the administrator's settings, or the defaults.
func (s Store) Settings() (Settings, error) {
	settings := DefaultSettings()
	if err := s.read("settings.json", &settings); err != nil {
		return Settings{}, err
	}
	if settings.Validate() != nil {
		return DefaultSettings(), nil
	}
	return settings, nil
}

// SaveSettings replaces the settings.
func (s Store) SaveSettings(settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	return s.write("settings.json", settings)
}

// PutRequest leaves a request for the next run of the service.
func (s Store) PutRequest(request Request) error { return s.write("request.json", request) }

// ErrStaleRequest is a request that waited for a run that never started.
var ErrStaleRequest = errors.New("a system update request no run took")

// TakeRequest returns the request the Host Agent left and removes it; ok is
// false when there is none. One left before notBefore waited for a run that
// never started: it is removed and refused with ErrStaleRequest, not carried
// out late, outside the install window and past the checks the Product
// Service made before asking. One that cannot be read is removed too, so
// that it fails one run, not every run.
func (s Store) TakeRequest(notBefore time.Time) (request Request, ok bool, err error) {
	path := s.path("request.json")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Request{}, false, nil
	}
	if err != nil {
		return Request{}, false, err
	}
	readErr := s.read("request.json", &request)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Request{}, false, err
	}
	if readErr != nil {
		return Request{}, false, readErr
	}
	if info.ModTime().Before(notBefore) {
		return Request{}, false, fmt.Errorf("%w: %s by %s, left at %s", ErrStaleRequest, request.Kind, request.By,
			info.ModTime().UTC().Format(time.RFC3339))
	}
	return request, true, nil
}

// DropRequest removes a request no run will take, as when the unit that
// would take it did not start.
func (s Store) DropRequest() error {
	if err := os.Remove(s.path("request.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// RequestWaiting reports whether a request was left within the last
// minute and no run has taken it yet: the unit that takes it is starting.
// An older one waited for a run that did not start; the next run drops it.
func (s Store) RequestWaiting(now time.Time) bool {
	info, err := os.Stat(s.path("request.json"))
	return err == nil && now.Sub(info.ModTime()) < time.Minute
}

func (s Store) read(name string, into any) error {
	data, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path(name), err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("read %s: %w", s.path(name), err)
	}
	return nil
}

func (s Store) write(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.path(name), append(data, '\n'), 0o600)
}
