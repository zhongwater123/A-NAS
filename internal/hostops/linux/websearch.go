package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/websearchswitch"
)

// The administrator's switch of web search (ADR 0030) is this file in the
// Host Agent's state directory: anas-search.service starts only while it is
// absent, so the installer's restart of every release leaves it off.
const (
	webSearchUnit   = "anas-search.service"
	webSearchMarker = "web-search-disabled"
)

func (e *Executor) webSearchMarker() string {
	return filepath.Join(e.systemRoot, "var", "lib", stateDirectoryName, webSearchMarker)
}

// WebSearch reports the switch and what systemd says of the service.
func (e *Executor) WebSearch(ctx context.Context) (websearchswitch.State, error) {
	_, err := os.Lstat(e.webSearchMarker())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return websearchswitch.State{}, fmt.Errorf("inspect the web search switch: %w", err)
	}
	state := websearchswitch.State{Enabled: err != nil}
	output, err := e.runner.Output(ctx, "systemctl", []string{"show", "--property=ActiveState", "--value", webSearchUnit}, "")
	if err != nil {
		return websearchswitch.State{}, commandError("read the web search service's state", err, output)
	}
	switch strings.TrimSpace(string(output)) {
	case "active", "reloading":
		state.Running = websearchswitch.Running
	case "activating":
		state.Running = websearchswitch.Starting
	case "failed":
		state.Running = websearchswitch.Failed
	default:
		state.Running = websearchswitch.Stopped
	}
	return state, nil
}

// SetWebSearch turns web search on or off and starts or stops the service.
func (e *Executor) SetWebSearch(ctx context.Context, enabled bool) (websearchswitch.State, error) {
	marker := e.webSearchMarker()
	action := "start"
	if enabled {
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return websearchswitch.State{}, fmt.Errorf("turn web search on: %w", err)
		}
	} else {
		action = "stop"
		if err := os.WriteFile(marker, []byte("Turned off in Settings; the Host Agent removes this file to turn web search on.\n"), 0o600); err != nil {
			return websearchswitch.State{}, fmt.Errorf("turn web search off: %w", err)
		}
	}
	if output, err := e.runner.Run(ctx, "systemctl", []string{action, "--no-block", webSearchUnit}, ""); err != nil {
		return websearchswitch.State{}, commandError(action+" the web search service", err, output)
	}
	return e.WebSearch(ctx)
}

var _ websearchswitch.Switch = (*Executor)(nil)
