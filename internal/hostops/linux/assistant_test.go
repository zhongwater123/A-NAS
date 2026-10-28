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
			for _, command := range []string{"disable --now", "mask --now"} {
				want := "systemctl " + command + " anas-assistant@alice.service anas-assistant-api@alice.service anas-assistant-api@alice.socket anas-assistant-cron@alice.service anas-assistant-curator@alice.service anas-assistant-curator@alice.timer"
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
	for _, action := range []string{"disable", "mask"} {
		if !host.ran("systemctl " + action + " --now anas-assistant-api@alice.socket") {
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
