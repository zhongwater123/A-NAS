package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// disableAssistant stops every way an assistant can run for a disabled member.
// Masks also reject a start queued by the waker just before the disable; disabling
// alone only removes boot/timer links and would still allow that start.
func (e *Executor) disableAssistant(ctx context.Context, username string) error {
	if _, err := os.Stat(filepath.Join(e.systemRoot, "etc", "systemd", "system", "anas-assistant@.service")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect assistant installation: %w", err)
	}
	units := []string{
		"anas-assistant@" + username + ".service",
		"anas-assistant-api@" + username + ".service",
		"anas-assistant-api@" + username + ".socket",
		"anas-assistant-cron@" + username + ".service",
		"anas-assistant-curator@" + username + ".service",
		"anas-assistant-curator@" + username + ".timer",
	}
	var failures []error
	for _, action := range []string{"disable", "mask"} {
		if output, err := e.runner.Run(ctx, "systemctl", append([]string{action, "--now"}, units...), ""); err != nil {
			failures = append(failures, commandError(action+" assistant units", err, output))
		}
	}
	return errors.Join(failures...)
}
