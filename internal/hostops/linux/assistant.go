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
// Masks also reject a start queued by the waker just before the disable; disabling
// alone only removes boot/timer links and would still allow that start.
func (e *Executor) disableAssistant(ctx context.Context, username string) error {
	templates := []string{
		"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-api@.socket",
		"anas-assistant-cron@.service", "anas-assistant-curator@.service", "anas-assistant-curator@.timer",
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
	for _, action := range []string{"disable", "mask"} {
		if output, err := e.runner.Run(ctx, "systemctl", append([]string{action, "--now"}, units...), ""); err != nil {
			failures = append(failures, commandError(action+" assistant units", err, output))
		}
	}
	return errors.Join(failures...)
}
