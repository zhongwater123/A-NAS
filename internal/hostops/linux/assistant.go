package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// disableAssistant stops every way an assistant can run for a disabled member.
//
// Starts keep arriving while it runs: the Product Service keeps a signed-in
// member's instance (its socket starts the instance again), and the waker
// starts due tasks. In systemd a start queued after a stop job replaces it,
// and systemctl then fails with "Job for … canceled" while the unit runs on.
// So the units are masked before anything is stopped: from then on systemd
// refuses those starts, and the stop jobs complete. Disabling, which starts no
// jobs, comes first: systemctl leaves the boot and timer links of a unit that
// is already masked.
func (e *Executor) disableAssistant(ctx context.Context, username string) error {
	templates := []string{
		"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-api@.socket",
		"anas-assistant-cron@.service", "anas-assistant-curator@.service", "anas-assistant-curator@.timer",
		// The member's private network with its bridge to the model, which the
		// units above need: without them it would stop by itself, but only
		// after this returns, and it still runs as the member.
		"anas-assistant-net@.service",
	}
	var units []string
	for _, template := range templates {
		if _, err := os.Stat(filepath.Join(e.systemRoot, "etc", "systemd", "system", template)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect assistant installation: %w", err)
		}
		units = append(units, strings.Replace(template, "@.", "@"+username+".", 1))
	}
	if len(units) == 0 {
		return nil
	}
	var failures []error
	for _, args := range [][]string{{"disable"}, {"mask", "--now"}} {
		if output, err := e.runner.Run(ctx, "systemctl", append(args, units...), ""); err != nil {
			failures = append(failures, commandError(args[0]+" assistant units", err, output))
		}
	}
	return errors.Join(failures...)
}
