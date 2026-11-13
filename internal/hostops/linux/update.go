package linux

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/systemupdate"
	"github.com/zhongwater123/A-NAS/internal/systemupdate/updater"
)

// The system update service's unit (docs/specs/system-update.md): it runs
// apart from the Host Agent, which an install restarts.
const updateUnit = "anas-update.service"

func (e *Executor) updateStore() updater.Store {
	return updater.Store{Dir: filepath.Join(e.systemRoot, "var", "lib", "a-nas-update")}
}

// updateRunning reports whether a run of the service is in progress: a
// oneshot unit is activating until its run ends.
func (e *Executor) updateRunning(ctx context.Context) (bool, error) {
	output, err := e.runner.Output(ctx, "systemctl", []string{"show", "--property=ActiveState", "--value", updateUnit}, "")
	if err != nil {
		return false, commandError("read the system update service's state", err, output)
	}
	state := strings.TrimSpace(string(output))
	return state == "activating" || state == "active" || state == "deactivating" || state == "reloading", nil
}

// SystemUpdate reads the state the service left. A state that says an install
// is under way while no run is in progress was left by a run that stopped;
// the service is started to finish it.
func (e *Executor) SystemUpdate(ctx context.Context) (systemupdate.Status, error) {
	e.updateMu.Lock()
	defer e.updateMu.Unlock()
	state, err := e.updateStore().Load()
	if err != nil {
		return systemupdate.Status{}, err
	}
	running, err := e.updateRunning(ctx)
	if err != nil {
		return systemupdate.Status{}, err
	}
	if state.Phase.Busy() && !running {
		if output, err := e.runner.Run(ctx, "systemctl", []string{"start", "--no-block", updateUnit}, ""); err != nil {
			return systemupdate.Status{}, commandError("finish the stopped system update", err, output)
		}
		running = true
	}
	return systemupdate.Status{State: state, Running: running}, nil
}

// RequestSystemUpdate leaves the request for the service and starts it.
func (e *Executor) RequestSystemUpdate(ctx context.Context, request systemupdate.Request) (systemupdate.Status, error) {
	if !systemupdate.Requestable(request.Kind) {
		return systemupdate.Status{}, fmt.Errorf("no such system update request: %q", request.Kind)
	}
	e.updateMu.Lock()
	defer e.updateMu.Unlock()
	running, err := e.updateRunning(ctx)
	if err != nil {
		return systemupdate.Status{}, err
	}
	if running {
		return systemupdate.Status{}, systemupdate.ErrBusy
	}
	store := e.updateStore()
	if err := store.PutRequest(request); err != nil {
		return systemupdate.Status{}, fmt.Errorf("leave the system update request: %w", err)
	}
	if output, err := e.runner.Run(ctx, "systemctl", []string{"start", "--no-block", updateUnit}, ""); err != nil {
		return systemupdate.Status{}, commandError("start the system update", err, output)
	}
	state, err := store.Load()
	if err != nil {
		return systemupdate.Status{}, err
	}
	return systemupdate.Status{State: state, Running: true}, nil
}

// SetSystemUpdateSettings replaces the administrator's settings, which the
// service reads on its next run.
func (e *Executor) SetSystemUpdateSettings(ctx context.Context, settings systemupdate.Settings) (systemupdate.Status, error) {
	if err := e.updateStore().SaveSettings(settings); err != nil {
		return systemupdate.Status{}, err
	}
	return e.SystemUpdate(ctx)
}

var _ systemupdate.Updater = (*Executor)(nil)
