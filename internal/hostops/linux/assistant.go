package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// assistantTemplates are the assistant's unit templates (ADR 0024).
var assistantTemplates = []string{
	"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-api@.socket",
	"anas-assistant-cron@.service", "anas-assistant-curator@.service", "anas-assistant-curator@.timer",
	// The member's private network with its bridge to the model, which the
	// units above need: without them it would stop by itself, but only
	// after this returns, and it still runs as the member.
	"anas-assistant-net@.service",
}

// assistantEntries are the units that start a member's assistant: the API
// socket the File Broker connects to and the weekly curator timer. The others
// run only when these start them (assistant-layout.sh enables the same two).
var assistantEntries = []string{"anas-assistant-api@.socket", "anas-assistant-curator@.timer"}

// assistantUnits names username's instances of the installed templates among
// templates; none when the assistant is not installed.
func (e *Executor) assistantUnits(username string, templates []string) ([]string, error) {
	var units []string
	for _, template := range templates {
		if _, err := os.Stat(filepath.Join(e.systemRoot, "etc", "systemd", "system", template)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("inspect assistant installation: %w", err)
		}
		units = append(units, strings.Replace(template, "@.", "@"+username+".", 1))
	}
	return units, nil
}

// assistantTokenPath is the member's instance secret, which assistant-layout.sh
// writes when it prepares the member's assistant.
func (e *Executor) assistantTokenPath(username string) string {
	return filepath.Join(e.systemRoot, "etc", "a-nas-assistant", "tokens", username+".env")
}

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
	units, err := e.assistantUnits(username, assistantTemplates)
	if err != nil || len(units) == 0 {
		return err
	}
	var failures []error
	for _, args := range [][]string{{"disable"}, {"mask", "--now"}} {
		if output, err := e.runner.Run(ctx, "systemctl", append(args, units...), ""); err != nil {
			failures = append(failures, commandError(args[0]+" assistant units", err, output))
		}
	}
	return errors.Join(failures...)
}

// enableAssistant undoes disableAssistant for an active member: it unmasks
// the units and, when the member's assistant has been prepared (its instance
// secret exists), enables its entry points as assistant-layout.sh does. A
// member the layout has not prepared yet gets them from its next run.
func (e *Executor) enableAssistant(ctx context.Context, username string) error {
	units, err := e.assistantUnits(username, assistantTemplates)
	if err != nil || len(units) == 0 {
		return err
	}
	if output, err := e.runner.Run(ctx, "systemctl", append([]string{"unmask"}, units...), ""); err != nil {
		return commandError("unmask assistant units", err, output)
	}
	if _, err := os.Stat(e.assistantTokenPath(username)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect assistant secret: %w", err)
	}
	entries, err := e.assistantUnits(username, assistantEntries)
	if err != nil || len(entries) == 0 {
		return err
	}
	if output, err := e.runner.Run(ctx, "systemctl", append([]string{"enable", "--now", "--quiet"}, entries...), ""); err != nil {
		return commandError("enable assistant units", err, output)
	}
	return nil
}
