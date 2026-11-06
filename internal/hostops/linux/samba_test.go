package linux_test

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
)

// sambaRunner answers testparm's questions about one parameter from the
// configuration file itself, as testparm reads a setting in the global section,
// and fails the command named in fail.
type sambaRunner struct {
	recordingRunner
	fail string
}

func (r *sambaRunner) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	if _, err := r.recordingRunner.Run(ctx, name, args, stdin); err != nil {
		return nil, err
	}
	if name == r.fail {
		return []byte(name + " failed"), errors.New("exit status 1")
	}
	return nil, nil
}

func (r *sambaRunner) Output(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	if _, err := r.Run(ctx, name, args, stdin); err != nil || name != "testparm" {
		return nil, err
	}
	parameter, ok := strings.CutPrefix(args[len(args)-2], "--parameter-name=")
	if !ok {
		return nil, nil
	}
	contents, err := os.ReadFile(args[len(args)-1])
	if err != nil {
		return nil, err
	}
	for line := range strings.Lines(string(contents)) {
		if key, value, found := strings.Cut(line, "="); found && strings.TrimSpace(key) == parameter {
			return []byte(strings.TrimSpace(value) + "\n"), nil
		}
	}
	return []byte("\n"), nil
}

// smbdCommands are the commands that act on the running smbd.
func (r *sambaRunner) smbdCommands() []string {
	var commands []string
	for _, line := range r.lines() {
		if strings.HasPrefix(line, "smbcontrol ") || strings.HasPrefix(line, "systemctl ") {
			commands = append(commands, line)
		}
	}
	return commands
}

const (
	reloadSamba  = "smbcontrol all reload-config"
	restartSamba = "systemctl try-restart --no-block smbd.service"
)

// smbd opens its listening sockets only when it starts, so the Host Agent
// restarts it when the addresses or ports it listens on change, and otherwise
// only has it reload, which keeps the open SMB sessions
// (docs/investigations/2026-11-04-smbd-starts-before-dhcp-address.md).
func TestSambaRestartsOnlyWhenWhereItListensChanges(t *testing.T) {
	debian := "[global]\n   workgroup = WORKGROUP\n   server role = standalone server\n"
	for _, test := range []struct {
		name     string
		previous string // "" when there is no configuration yet
		smb      string // the SMB interface of the second configuration
		want     []string
	}{
		{name: "first configuration", want: []string{restartSamba, reloadSamba}},
		{name: "Debian's configuration before it", previous: debian, want: []string{restartSamba, reloadSamba}},
		{name: "another SMB interface", smb: "enp4s0", want: []string{restartSamba, restartSamba}},
	} {
		t.Run(test.name, func(t *testing.T) {
			systemRoot := t.TempDir()
			configuration := filepath.Join(systemRoot, "etc", "samba", "smb.conf")
			if test.previous != "" {
				if err := os.MkdirAll(filepath.Dir(configuration), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configuration, []byte(test.previous), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			runner := &sambaRunner{}
			reconcile := func(smb string) {
				t.Helper()
				executor := linux.NewExecutor(nil, runner, linux.Options{
					SystemRoot: systemRoot, MountPoint: filepath.Join(t.TempDir(), "offline"), SMBInterface: smb,
				})
				if _, err := executor.ReconcileDataVolume(context.Background()); err != nil {
					t.Fatalf("ReconcileDataVolume() error = %v", err)
				}
			}
			reconcile("enp3s0")
			// The Host Agent's next start writes the same configuration, or
			// one for the interface the installer was given since.
			reconcile(cmp.Or(test.smb, "enp3s0"))
			if got := runner.smbdCommands(); !slices.Equal(got, test.want) {
				t.Fatalf("commands on smbd = %q, want %q", got, test.want)
			}
		})
	}
}

// A configuration smbd could not take is taken back, whether smbd was to
// reload or to restart for it.
func TestSambaConfigurationIsTakenBackWhenSmbdCannotTakeIt(t *testing.T) {
	for _, test := range []struct {
		name, fail string
		previous   string
	}{
		{name: "restart", fail: "systemctl", previous: "[global]\n   workgroup = WORKGROUP\n"},
		{name: "reload", fail: "smbcontrol", previous: "[global]\n   interfaces = lo enp3s0\n   bind interfaces only = yes\n   smb ports = 445\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			systemRoot := t.TempDir()
			configuration := filepath.Join(systemRoot, "etc", "samba", "smb.conf")
			if err := os.MkdirAll(filepath.Dir(configuration), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configuration, []byte(test.previous), 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &sambaRunner{fail: test.fail}
			executor := linux.NewExecutor(nil, runner, linux.Options{
				SystemRoot: systemRoot, MountPoint: filepath.Join(t.TempDir(), "offline"), SMBInterface: "enp3s0",
			})
			if _, err := executor.ReconcileDataVolume(context.Background()); err == nil || !strings.Contains(err.Error(), test.fail+" failed") {
				t.Fatalf("ReconcileDataVolume() error = %v, want the %s failure", err, test.fail)
			}
			if contents, err := os.ReadFile(configuration); err != nil || string(contents) != test.previous {
				t.Fatalf("configuration = %q, %v; want the previous one back", contents, err)
			}
		})
	}
}
