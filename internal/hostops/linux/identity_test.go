package linux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
)

func TestSetCredentialCreatesIdentityInTheReservedRange(t *testing.T) {
	host := newFakeHost()
	systemRoot := t.TempDir()
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: systemRoot, MountPoint: filepath.Join(t.TempDir(), "data")})

	err := executor.SetCredential(context.Background(), accounts.CredentialRequest{
		UserID: "user:owner", PrivateSpaceID: "space:owner", Username: "owner",
		Password: "owner password for testing", Role: accounts.RoleAdmin, UID: 20100, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}
	for _, want := range []string{
		"groupadd --gid 20000 a-nas-users",
		"groupadd --gid 20001 a-nas-admins",
		"groupadd --gid 20100 owner",
		"useradd --uid 20100 --gid 20100 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin owner",
		"usermod --groups a-nas-users,a-nas-admins owner",
	} {
		if !host.ran(want) {
			t.Errorf("missing command %q; got:\n%s", want, strings.Join(host.commands, "\n"))
		}
	}
	registry, err := os.ReadFile(filepath.Join(systemRoot, "var", "lib", "a-nas", "identity-registry.json"))
	if err != nil {
		t.Fatalf("read identity registry: %v", err)
	}
	var records map[string]struct {
		UID     int  `json:"uid"`
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(registry, &records); err != nil || records["owner"].UID != 20100 || !records["owner"].Enabled {
		t.Fatalf("identity registry = %s (err %v), want owner with UID 20100", registry, err)
	}
}

func TestSetCredentialRefusesToAdoptAccountsOutsideTheRange(t *testing.T) {
	for _, test := range []struct {
		name     string
		username string
		prepare  func(*fakeHost)
	}{
		{name: "system account", username: "anas-dev", prepare: func(h *fakeHost) {
			h.users["anas-dev"] = [2]int{1000, 1000}
			h.groups["anas-dev"] = 1000
		}},
		{name: "service account", username: "a-nas", prepare: func(h *fakeHost) {
			h.users["a-nas"] = [2]int{998, 998}
		}},
		{name: "UID already used by another name", username: "alice", prepare: func(h *fakeHost) {
			h.users["mallory"] = [2]int{20101, 20101}
		}},
		{name: "group with a foreign GID", username: "alice", prepare: func(h *fakeHost) {
			h.groups["alice"] = 1500
		}},
		{name: "fixed group name", username: "a-nas-users", prepare: func(*fakeHost) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newFakeHost()
			test.prepare(host)
			executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})

			err := executor.SetCredential(context.Background(), accounts.CredentialRequest{
				UserID: "user:x", PrivateSpaceID: "space:x", Username: test.username,
				Password: "a password for testing", Role: accounts.RoleMember, UID: 20101, Enabled: true,
			})
			if !errors.Is(err, accounts.ErrIdentityConflict) {
				t.Fatalf("SetCredential() error = %v, want ErrIdentityConflict", err)
			}
			for _, command := range host.commands {
				if strings.HasPrefix(command, "useradd") || strings.HasPrefix(command, "usermod") || strings.HasPrefix(command, "smbpasswd") {
					t.Fatalf("conflicting account was modified: %q", command)
				}
			}
		})
	}
}

func TestSetCredentialRejectsUIDOutsideTheRange(t *testing.T) {
	for _, uid := range []int{0, 1000, accounts.UsersGID, accounts.FirstUserUID - 1, accounts.LastUserUID + 1} {
		host := newFakeHost()
		executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})
		err := executor.SetCredential(context.Background(), accounts.CredentialRequest{
			UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice",
			Password: "a password for testing", Role: accounts.RoleMember, UID: uid, Enabled: true,
		})
		if err == nil || len(host.commands) != 0 {
			t.Fatalf("UID %d: error = %v, commands = %v; want rejection before any host command", uid, err, host.commands)
		}
	}
}

func TestSetCredentialKeepsDisabledAccountDisabled(t *testing.T) {
	host := newFakeHost()
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})

	err := executor.SetCredential(context.Background(), accounts.CredentialRequest{
		UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice",
		Password: "a password for testing", Role: accounts.RoleMember, UID: 20100, Enabled: false,
	})
	if err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}
	add := slices.Index(host.commands, "smbpasswd -s -a alice")
	disable := slices.Index(host.commands, "smbpasswd -d alice")
	if add < 0 || disable < add {
		t.Fatalf("Samba credential was not re-disabled after the reset:\n%s", strings.Join(host.commands, "\n"))
	}
}

func TestDisableCredentialDisconnectsEstablishedSMBSessions(t *testing.T) {
	host := newFakeHost()
	host.smbstatus = `{"sessions": {
		"1": {"session_id": "1", "server_id": {"pid": "42"}, "username": "alice"},
		"2": {"session_id": "2", "server_id": {"pid": "42"}, "username": "alice"},
		"3": {"session_id": "3", "server_id": {"pid": "77"}, "username": "bob"}}}`
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})

	if err := executor.DisableCredential(context.Background(), "alice"); err != nil {
		t.Fatalf("DisableCredential() error = %v", err)
	}
	for _, want := range []string{"smbpasswd -d alice", "smbstatus --processes --user=alice --json", "smbcontrol 42 shutdown"} {
		if !host.ran(want) {
			t.Errorf("missing command %q; got:\n%s", want, strings.Join(host.commands, "\n"))
		}
	}
	if host.ran("smbcontrol 77 shutdown") || strings.Count(strings.Join(host.commands, "\n"), "smbcontrol 42") != 1 {
		t.Fatalf("disconnected the wrong sessions:\n%s", strings.Join(host.commands, "\n"))
	}
}

func TestSyncIdentitiesContinuesPastAConflictingAccount(t *testing.T) {
	host := newFakeHost()
	host.users["admin"] = [2]int{999, 999}
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})

	err := executor.SyncIdentities(context.Background(), []accounts.Identity{
		{Username: "admin", UID: 20100, Role: accounts.RoleAdmin, Enabled: true},
		{Username: "alice", UID: 20101, Role: accounts.RoleMember, Enabled: true},
	})
	if !errors.Is(err, accounts.ErrIdentityConflict) {
		t.Fatalf("SyncIdentities() error = %v, want ErrIdentityConflict", err)
	}
	if !host.ran("usermod --groups a-nas-users alice") {
		t.Fatalf("a conflict stopped other identities from converging:\n%s", strings.Join(host.commands, "\n"))
	}
	if host.ran("usermod --groups a-nas-users,a-nas-admins admin") {
		t.Fatal("conflicting legacy account was modified")
	}
}

func TestSyncIdentitiesConvergesRolesAndDisabledAccounts(t *testing.T) {
	host := newFakeHost()
	host.users["owner"] = [2]int{20100, 20100}
	host.groups["owner"] = 20100
	host.samba["bob"] = true
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})

	err := executor.SyncIdentities(context.Background(), []accounts.Identity{
		{Username: "owner", UID: 20100, Role: accounts.RoleAdmin, Enabled: true},
		{Username: "bob", UID: 20101, Role: accounts.RoleMember, Enabled: false},
	})
	if err != nil {
		t.Fatalf("SyncIdentities() error = %v", err)
	}
	for _, want := range []string{
		"usermod --groups a-nas-users,a-nas-admins owner",
		"useradd --uid 20101 --gid 20101 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin bob",
		"usermod --groups a-nas-users bob",
		"smbpasswd -d bob",
	} {
		if !host.ran(want) {
			t.Errorf("missing command %q; got:\n%s", want, strings.Join(host.commands, "\n"))
		}
	}
	if host.ran("useradd --uid 20100 --gid 20100 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin owner") {
		t.Fatal("existing identity was recreated")
	}
	for _, command := range host.commands {
		if strings.HasPrefix(command, "smbpasswd -s") {
			t.Fatalf("identity synchronization changed a password: %q", command)
		}
	}
}

type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

// fakeHost simulates the passwd, group, and Samba databases that the identity
// code reads through getent and pdbedit.
type fakeHost struct {
	users     map[string][2]int
	groups    map[string]int
	samba     map[string]bool
	smbstatus string
	commands  []string
}

func newFakeHost() *fakeHost {
	return &fakeHost{users: map[string][2]int{"root": {0, 0}}, groups: map[string]int{"root": 0}, samba: map[string]bool{}}
}

func (h *fakeHost) ran(command string) bool { return slices.Contains(h.commands, command) }

func (h *fakeHost) Run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	h.commands = append(h.commands, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	switch name {
	case "getent":
		return h.getent(args[0], args[1])
	case "groupadd":
		if args[0] == "--gid" {
			var gid int
			_, _ = fmt.Sscan(args[1], &gid)
			h.groups[args[2]] = gid
		}
	case "useradd":
		var uid, gid int
		_, _ = fmt.Sscan(args[1], &uid)
		_, _ = fmt.Sscan(args[3], &gid)
		h.users[args[len(args)-1]] = [2]int{uid, gid}
	case "pdbedit":
		if !h.samba[args[1]] {
			return nil, exitStatus(255)
		}
	case "smbpasswd":
		if args[0] == "-s" {
			h.samba[args[2]] = true
		}
	case "smbstatus":
		return []byte(h.smbstatus), nil
	}
	return nil, nil
}

func (h *fakeHost) getent(database, key string) ([]byte, error) {
	switch database {
	case "passwd":
		for name, ids := range h.users {
			if name == key || fmt.Sprint(ids[0]) == key {
				return []byte(fmt.Sprintf("%s:x:%d:%d::/nonexistent:/usr/sbin/nologin\n", name, ids[0], ids[1])), nil
			}
		}
	case "group":
		for name, gid := range h.groups {
			if name == key || fmt.Sprint(gid) == key {
				return []byte(fmt.Sprintf("%s:x:%d:\n", name, gid)), nil
			}
		}
	}
	return nil, exitStatus(2)
}
