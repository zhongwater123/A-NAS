package linux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
)

var assistantUnits = []string{
	"anas-assistant@alice.service", "anas-assistant-api@alice.service", "anas-assistant-api@alice.socket",
	"anas-assistant-cron@alice.service", "anas-assistant-curator@alice.service", "anas-assistant-curator@alice.timer",
}

// installedAssistant returns a system root with every assistant template.
func installedAssistant(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	units := filepath.Join(root, "etc", "systemd", "system")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, template := range []string{"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-api@.socket", "anas-assistant-cron@.service", "anas-assistant-curator@.service", "anas-assistant-curator@.timer"} {
		if err := os.WriteFile(filepath.Join(units, template), []byte("[Service]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type unavailableSamba struct{ *fakeHost }

func (h unavailableSamba) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	if name == "smbpasswd" {
		return nil, errors.New("Samba is unavailable")
	}
	return h.fakeHost.Run(ctx, name, args, stdin)
}

func TestDisabledAccountsStopAndMaskAssistantUnits(t *testing.T) {
	for _, operation := range []string{"disable", "sync", "Samba failure"} {
		t.Run(operation, func(t *testing.T) {
			root := installedAssistant(t)
			host := newFakeHost()
			var runner linux.CommandRunner = host
			if operation == "Samba failure" {
				runner = unavailableSamba{host}
			}
			executor := linux.NewExecutor(nil, runner, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
			var err error
			if operation == "sync" {
				err = executor.SyncIdentities(context.Background(), []accounts.Identity{{Username: "alice", UID: 20100, Role: accounts.RoleMember}})
			} else {
				err = executor.DisableCredential(context.Background(), "alice")
			}
			if operation == "Samba failure" {
				if err == nil {
					t.Fatal("Samba failure must still be reported")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"disable", "mask --now"} {
				want := "systemctl " + command + " " + strings.Join(assistantUnits, " ")
				if !host.ran(want) {
					t.Errorf("assistant not fenced (%s):\n%s", command, strings.Join(host.commands, "\n"))
				}
			}
		})
	}
}

func TestDisableAssistantWithOnlyOptionalSocketInstalled(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "etc", "systemd", "system")
	if err := os.MkdirAll(units, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "anas-assistant-api@.socket"), []byte("[Socket]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	host := newFakeHost()
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
	if err := executor.DisableCredential(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"disable", "mask --now"} {
		if !host.ran("systemctl " + command + " anas-assistant-api@alice.socket") {
			t.Fatalf("commands: %v", host.commands)
		}
	}
	smb, stop := -1, -1
	for i, command := range host.commands {
		if strings.HasPrefix(command, "smbpasswd") {
			smb = i
		}
		if strings.HasPrefix(command, "systemctl disable") {
			stop = i
		}
	}
	if smb < 0 || stop < smb {
		t.Fatalf("SMB must be revoked first: %v", host.commands)
	}
}

// keptSystemd is systemd while the Product Service keeps alice's instance
// running: whenever systemctl stops units, the keeper's next connection to
// the member socket starts the instance again. As in systemd, that start
// replaces the stop jobs of units that are not masked yet, systemctl reports
// them canceled, and the instance runs on; a masked unit refuses the start.
// Like systemctl, disable leaves the links of a unit that is already masked.
type keptSystemd struct {
	*fakeHost
	masked  map[string]bool
	linked  map[string]bool
	running map[string]bool
	// stuck is a unit whose stop fails for a reason of its own.
	stuck string
}

func newKeptSystemd() *keptSystemd {
	h := &keptSystemd{fakeHost: newFakeHost(), masked: map[string]bool{}, linked: map[string]bool{}, running: map[string]bool{}}
	for _, unit := range []string{"anas-assistant-api@alice.socket", "anas-assistant-curator@alice.timer"} {
		h.linked[unit], h.running[unit] = true, true
	}
	h.running["anas-assistant@alice.service"] = true
	h.running["anas-assistant-api@alice.service"] = true
	return h
}

func (h *keptSystemd) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	output, err := h.fakeHost.Run(ctx, name, args, stdin)
	if name != "systemctl" || err != nil {
		return output, err
	}
	now, units := false, []string{}
	for _, arg := range args[1:] {
		if arg == "--now" {
			now = true
		} else {
			units = append(units, arg)
		}
	}
	for _, unit := range units {
		switch args[0] {
		case "disable":
			if !h.masked[unit] {
				delete(h.linked, unit)
			}
		case "mask":
			h.masked[unit] = true
		}
	}
	if !now {
		return output, nil
	}
	var failed []string
	for _, unit := range units {
		switch {
		case unit == h.stuck:
			failed = append(failed, "Job for "+unit+" failed because the control process exited with error code.")
		case (unit == "anas-assistant@alice.service" || unit == "anas-assistant-api@alice.service") && !h.masked[unit]:
			failed = append(failed, "Job for "+unit+" canceled.")
		default:
			delete(h.running, unit)
		}
	}
	if len(failed) > 0 {
		return append(output, strings.Join(failed, "\n")...), exitStatus(1)
	}
	return output, nil
}

func TestDisablingAnAssistantThatIsBeingKeptRunning(t *testing.T) {
	host := newKeptSystemd()
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: installedAssistant(t), MountPoint: filepath.Join(t.TempDir(), "data")})
	if err := executor.DisableCredential(context.Background(), "alice"); err != nil {
		t.Fatalf("DisableCredential() error = %v\ncommands:\n%s", err, strings.Join(host.commands, "\n"))
	}
	for _, unit := range assistantUnits {
		if !host.masked[unit] || host.running[unit] || host.linked[unit] {
			t.Errorf("%s: masked %v, running %v, linked %v", unit, host.masked[unit], host.running[unit], host.linked[unit])
		}
	}
	// Disabling the account again, and the identity synchronization, find
	// every unit masked already and succeed.
	if err := executor.DisableCredential(context.Background(), "alice"); err != nil {
		t.Fatalf("retried DisableCredential() error = %v", err)
	}
	if err := executor.SyncIdentities(context.Background(), []accounts.Identity{{Username: "alice", UID: 20100, Role: accounts.RoleMember}}); err != nil {
		t.Fatalf("SyncIdentities() error = %v", err)
	}
}

func TestDisablingReportsAUnitThatDidNotStop(t *testing.T) {
	host := newKeptSystemd()
	host.stuck = "anas-assistant-cron@alice.service"
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: installedAssistant(t), MountPoint: filepath.Join(t.TempDir(), "data")})
	err := executor.DisableCredential(context.Background(), "alice")
	if err == nil || !strings.Contains(err.Error(), "mask assistant units") || !strings.Contains(err.Error(), "Job for anas-assistant-cron@alice.service failed") {
		t.Fatalf("DisableCredential() error = %v, want the failed stop", err)
	}
	if !host.masked["anas-assistant-cron@alice.service"] {
		t.Fatal("the unit that did not stop must still be masked")
	}
}
