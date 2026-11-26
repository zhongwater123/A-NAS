package linux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
)

var assistantUnits = []string{
	"anas-assistant@alice.service", "anas-assistant-api@alice.service", "anas-assistant-api@alice.socket",
	"anas-assistant-cron@alice.service", "anas-assistant-curator@alice.service", "anas-assistant-curator@alice.timer",
	"anas-assistant-plan@alice.service", "anas-assistant-planapi@alice.service", "anas-assistant-planapi@alice.socket",
	"anas-assistant-net@alice.service",
}

// installedAssistant returns a system root with every assistant template and the files of
// the assistant's library that the Host Agent reads.
func installedAssistant(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	units := filepath.Join(root, "etc", "systemd", "system")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(root, "usr", "lib", "a-nas", "assistant")
	if err := os.MkdirAll(filepath.Join(library, "managed"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy := "mcp_servers:\n  anas:\n    trust: untrusted\n"
	for name, contents := range map[string]string{"managed/config.yaml": policy, "member-skill.md": "{{member}}\n"} {
		if err := os.WriteFile(filepath.Join(library, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, template := range []string{"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-api@.socket", "anas-assistant-cron@.service",
		"anas-assistant-curator@.service", "anas-assistant-curator@.timer", "anas-assistant-plan@.service", "anas-assistant-planapi@.service",
		"anas-assistant-planapi@.socket", "anas-assistant-net@.service"} {
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
	for _, unit := range []string{"anas-assistant-api@alice.socket", "anas-assistant-planapi@alice.socket", "anas-assistant-curator@alice.timer"} {
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

// credsHost is the fake host with systemd-creds, which answers what it encrypts, systemctl
// show, which reports the model gateway running, and systemctl commands that fail as many
// times as cancelled says, as a stop does whose job a start replaced.
type credsHost struct {
	*fakeHost
	cancelled map[string]int
}

func (h credsHost) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	command := name + " " + strings.Join(args, " ")
	if h.cancelled[command] > 0 {
		h.cancelled[command]--
		h.commands = append(h.commands, command)
		return []byte("Job for anas-assistant@alice.service canceled."), errors.New("exit status 1")
	}
	return h.fakeHost.Run(ctx, name, args, stdin)
}

func (h credsHost) Output(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	switch {
	case name == "systemd-creds":
		h.commands = append(h.commands, name+" "+strings.Join(args, " "))
		return []byte("encrypted:" + stdin), nil
	case name == "systemctl" && args[0] == "show":
		h.commands = append(h.commands, name+" "+strings.Join(args, " "))
		return []byte("active\n"), nil
	}
	return h.fakeHost.Output(ctx, name, args, stdin)
}

func TestTheAssistantKeyTurnsEveryAssistantOnAndOff(t *testing.T) {
	root := installedAssistant(t)
	host := credsHost{newFakeHost(), map[string]int{}}
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
	ctx := context.Background()
	if err := executor.SyncIdentities(ctx, []accounts.Identity{
		{Username: "alice", UID: 20100, Role: accounts.RoleMember, Enabled: true},
		{Username: "bob", UID: 20101, Role: accounts.RoleMember},
	}); err != nil {
		t.Fatal(err)
	}
	if host.ran("systemctl enable --now --quiet anas-assistant-api@alice.socket anas-assistant-planapi@alice.socket anas-assistant-curator@alice.timer") {
		t.Fatal("without a key no assistant is turned on")
	}
	state, err := executor.AssistantKey(ctx)
	if err != nil || state.Configured {
		t.Fatalf("AssistantKey() = %+v, %v", state, err)
	}
	if _, err := executor.SetAssistantKey(ctx, "sk-short"); err == nil {
		t.Fatal("a key that cannot be DeepSeek's was saved")
	}
	key := "sk-0123456789abcdef0123456789abcdef"
	state, err = executor.SetAssistantKey(ctx, key)
	if err != nil || !state.Configured || state.Gateway != "running" {
		t.Fatalf("SetAssistantKey() = %+v, %v", state, err)
	}
	credential := filepath.Join(root, "etc", "credstore.encrypted", "a-nas-assistant-deepseek")
	contents, err := os.ReadFile(credential)
	if err != nil || string(contents) != "encrypted:"+key {
		t.Fatalf("credential %q, %v", contents, err)
	}
	if info, _ := os.Stat(credential); info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode %v", info.Mode())
	}
	for _, command := range []string{
		"systemd-creds encrypt --name=a-nas-assistant-deepseek --with-key=host - -",
		"systemctl restart --no-block anas-assistant-gateway.service",
		"systemctl start --no-block anas-assistant-cron-waker.service",
		"systemctl start --no-block anas-videocut-planner.service",
		"systemctl enable --now --quiet anas-assistant-api@alice.socket anas-assistant-planapi@alice.socket anas-assistant-curator@alice.timer",
	} {
		if !host.ran(command) {
			t.Errorf("did not run %q:\n%s", command, strings.Join(host.commands, "\n"))
		}
	}
	for _, command := range host.commands {
		if strings.Contains(command, key) {
			t.Fatalf("the key is on a command line: %s", command)
		}
	}
	// A start queued while alice's assistant stops (立即运行, her socket) replaces the stop
	// job once; without the key it runs nothing, and stopping again finds it stopped.
	stopAlice := "systemctl stop anas-assistant@alice.service anas-assistant-api@alice.service anas-assistant-cron@alice.service anas-assistant-curator@alice.service " +
		"anas-assistant-plan@alice.service anas-assistant-planapi@alice.service anas-assistant-net@alice.service"
	host.cancelled[stopAlice] = 1
	state, err = executor.DeleteAssistantKey(ctx)
	if err != nil || state.Configured {
		t.Fatalf("DeleteAssistantKey() = %+v, %v", state, err)
	}
	if _, err := os.Stat(credential); !os.IsNotExist(err) {
		t.Fatalf("the credential is still there: %v", err)
	}
	for _, command := range []string{
		"systemctl stop anas-assistant-cron-waker.service anas-videocut-planner.service anas-assistant-gateway.service",
		"systemctl disable --now --quiet anas-assistant-api@alice.socket anas-assistant-planapi@alice.socket anas-assistant-curator@alice.timer",
		stopAlice,
	} {
		if !host.ran(command) {
			t.Errorf("did not run %q:\n%s", command, strings.Join(host.commands, "\n"))
		}
	}
	stops := 0
	for _, command := range host.commands {
		if command == stopAlice {
			stops++
		}
	}
	if stops != 2 {
		t.Errorf("alice's assistant was stopped %d times, want again after the cancelled stop", stops)
	}
	for _, command := range host.commands {
		if strings.Contains(command, "@bob.") && !strings.HasPrefix(command, "systemctl disable anas-") && !strings.HasPrefix(command, "systemctl mask") {
			t.Errorf("a disabled member's assistant was touched: %s", command)
		}
	}
}

func TestPackagedAssistantUnitsAreFound(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "usr", "lib", "systemd", "system")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "anas-assistant-api@.socket"), []byte("[Socket]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := newFakeHost()
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
	if err := executor.DisableCredential(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if !host.ran("systemctl mask --now anas-assistant-api@alice.socket") {
		t.Fatalf("commands: %v", host.commands)
	}
}

// Masking a running socket unit replaces its configuration, so its RemoveOnStop= does not
// apply when the mask stops it: disabling removes the socket file it left, and nothing else.
func TestDisablingRemovesTheSocketTheMaskLeft(t *testing.T) {
	root := installedAssistant(t)
	sockets := filepath.Join(root, "run", "a-nas-assistant")
	if err := os.MkdirAll(sockets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(filepath.Join(sockets, "alice.sock"), unix.S_IFSOCK|0o600, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sockets, "bob.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	host := newFakeHost()
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
	for _, username := range []string{"alice", "bob"} {
		if err := executor.DisableCredential(context.Background(), username); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(filepath.Join(sockets, "alice.sock")); !os.IsNotExist(err) {
		t.Fatalf("alice's socket after disabling her: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(sockets, "bob.sock")); err != nil {
		t.Fatalf("a file that is no socket was removed: %v", err)
	}
}

// A planning grant for VideoCut is the member's own, left while they were signed
// in: closing the account removes it, so the waker starts no round of planning for it, and a
// later account of the same name does not inherit it. Nobody else's is touched, and it goes
// even where no assistant unit is installed.
func TestDisablingAnAccountRemovesItsPlanningGrant(t *testing.T) {
	for _, installed := range []bool{true, false} {
		root := t.TempDir()
		if installed {
			root = installedAssistant(t)
		}
		host := newFakeHost()
		executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
		plans := executor.AssistantPlanDir()
		if want := filepath.Join(root, "var", "lib", "a-nas-host", "assistant", "plans"); plans != want {
			t.Fatalf("planning grants in %s, want the Host Agent's own %s", plans, want)
		}
		if err := os.MkdirAll(plans, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, username := range []string{"alice", "bob"} {
			if err := os.WriteFile(filepath.Join(plans, username), []byte(`{"issuedAt":"2026-11-18T10:00:00Z","rounds":5}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := executor.DisableCredential(context.Background(), "alice"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(plans, "alice")); !os.IsNotExist(err) {
			t.Fatalf("installed %v: alice's planning grant after closing her account: %v", installed, err)
		}
		if _, err := os.Lstat(filepath.Join(plans, "bob")); err != nil {
			t.Fatalf("installed %v: bob's planning grant: %v", installed, err)
		}
	}
}
