//go:build rootintegration

package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// TestRootAdministrativeViewing grants and revokes real ACL entries on a
// Btrfs volume and checks access through the File Broker and a terminal.
func TestRootAdministrativeViewing(t *testing.T) {
	ctx := context.Background()
	mount := "/srv/a-nas/data"
	resetAgentState(t)
	setUpDataVolume(t, mount)
	startSamba(t)
	clock := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount, Now: now})
	identities := map[string]accounts.Identity{}
	for i, user := range []struct {
		name string
		role accounts.Role
	}{{"keeper", accounts.RoleAdmin}, {"alice", accounts.RoleMember}, {"bob", accounts.RoleMember}} {
		if err := executor.SetCredential(ctx, accounts.CredentialRequest{
			UserID: "user:" + user.name, PrivateSpaceID: "space:" + user.name, Username: user.name,
			Password: user.name + " password for tests", Role: user.role, UID: 20110 + i, Enabled: true,
		}); err != nil {
			t.Fatalf("SetCredential(%s) error = %v", user.name, err)
		}
		identities[user.name+"-token"] = accounts.Identity{Username: user.name, UID: 20110 + i, Role: user.role, Enabled: true}
	}
	web := startFileBroker(t, mount, executor, identities)
	as := func(user string) context.Context { return accounts.WithSessionToken(ctx, user+"-token") }
	private := filepath.Join(mount, "spaces", "private", "alice")
	expect := func(what string, allowed bool, result outcome) {
		t.Helper()
		if result.ok != allowed {
			t.Errorf("%s: allowed=%v, want %v\n%s", what, result.ok, allowed, result.output)
		}
	}
	webRead := func(user, path string) outcome {
		file, err := web.Open(as(user), path)
		if err != nil {
			return outcome{err.Error(), false}
		}
		_ = file.Close()
		return outcome{"", true}
	}
	webWrite := func(user, path string) outcome {
		file, temporary, err := web.CreateTemp(as(user), filepath.Dir(path), ".a-nas-upload-")
		if err != nil {
			return outcome{err.Error(), false}
		}
		_ = file.Close()
		if err := web.Rename(as(user), temporary, path); err != nil {
			return outcome{err.Error(), false}
		}
		return outcome{"", true}
	}
	expect("alice creates files", true, asUser("alice", "sh", "-c",
		"mkdir "+private+"/docs && echo a > "+private+"/docs/a.txt && echo b > "+private+"/b.txt"))
	maskBefore := maskOf(t, filepath.Join(private, "b.txt"))
	expect("admin Web reads before viewing", false, webRead("keeper", filepath.Join(private, "b.txt")))
	expiry := clock.Add(accounts.ViewingDuration)

	// Nothing would apply a grant made while the volume is offline.
	offline := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: filepath.Join(t.TempDir(), "offline"), Now: now})
	if err := offline.GrantViewing(ctx, accounts.ViewingRequest{
		ID: "viewing:offline", SpaceID: "space:alice", AdminUsername: "keeper", AdminUID: 20110, ExpiresAt: expiry,
	}); !errors.Is(err, accounts.ErrVolumeUnavailable) {
		t.Fatalf("GrantViewing() offline error = %v, want ErrVolumeUnavailable", err)
	}

	// A walk that fails part-way leaves no entries and no active grant.
	stuck := filepath.Join(private, "docs", "stuck.txt")
	expect("alice creates a file", true, asUser("alice", "sh", "-c", "echo s > "+stuck))
	run(t, "chattr", "+i", stuck)
	err := executor.GrantViewing(ctx, accounts.ViewingRequest{
		ID: "viewing:failed", SpaceID: "space:alice", AdminUsername: "keeper", AdminUID: 20110, ExpiresAt: expiry,
	})
	run(t, "chattr", "-i", stuck)
	if err == nil {
		t.Fatal("GrantViewing() on an immutable file succeeded")
	}
	if acl := asUser("root", "getfacl", "--recursive", "--absolute-names", private).output; strings.Contains(acl, "user:keeper") {
		t.Errorf("a failed grant left ACL entries behind:\n%s", acl)
	}
	if viewers := executor.viewersOf("space:alice"); len(viewers) != 0 {
		t.Errorf("a failed grant is active for %v", viewers)
	}
	run(t, "rm", stuck)

	if err := executor.GrantViewing(ctx, accounts.ViewingRequest{
		ID: "viewing:1", SpaceID: "space:alice", AdminUsername: "keeper", AdminUID: 20110, ExpiresAt: expiry,
	}); err != nil {
		t.Fatalf("GrantViewing() error = %v", err)
	}
	expect("admin Web reads a file", true, webRead("keeper", filepath.Join(private, "b.txt")))
	expect("admin Web reads a nested file", true, webRead("keeper", filepath.Join(private, "docs", "a.txt")))
	if entries, err := web.Scan(as("keeper"), private, files.ScanSkip{}); err != nil || len(entries) < 3 {
		t.Errorf("admin Web listing = %d entries, %v", len(entries), err)
	}
	expect("admin terminal reads", true, asUser("keeper", "cat", filepath.Join(private, "docs", "a.txt")))
	expect("admin Web uploads", false, webWrite("keeper", filepath.Join(private, "admin.txt")))
	expect("admin Web renames", false, outcomeOf(web.Rename(as("keeper"), filepath.Join(private, "b.txt"), filepath.Join(private, "c.txt"))))
	expect("admin Web deletes", false, outcomeOf(web.RemoveAll(as("keeper"), filepath.Join(private, "docs"))))
	expect("admin terminal appends", false, asUser("keeper", "sh", "-c", "echo x >> "+filepath.Join(private, "b.txt")))
	expect("bob still cannot read", false, webRead("bob", filepath.Join(private, "b.txt")))
	expect("alice still writes", true, webWrite("alice", filepath.Join(private, "during.txt")))
	expect("admin reads a file created during viewing", true, webRead("keeper", filepath.Join(private, "during.txt")))
	if got := maskOf(t, filepath.Join(private, "b.txt")); got != maskBefore {
		t.Errorf("viewing changed the file mask from %q to %q", maskBefore, got)
	}
	if repaired, err := executor.RepairDataVolumePermissions(ctx); err != nil || len(repaired) != 0 {
		t.Errorf("drift repair during viewing = %v, %v; want no changes", repaired, err)
	}

	// A restarted Host Agent revokes the grant on time.
	clock = expiry.Add(time.Minute)
	restarted := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount, Now: now})
	revoked, err := restarted.ExpireViewing(ctx)
	if err != nil || !slices.Equal(revoked, []string{"viewing:1"}) {
		t.Fatalf("ExpireViewing() = %v, %v; want the expired grant", revoked, err)
	}
	expect("admin Web reads after expiry", false, webRead("keeper", filepath.Join(private, "b.txt")))
	expect("admin terminal reads after expiry", false, asUser("keeper", "cat", filepath.Join(private, "docs", "a.txt")))
	if acl := asUser("root", "getfacl", "--recursive", "--absolute-names", private).output; strings.Contains(acl, "user:keeper") {
		t.Errorf("revocation left ACL entries behind:\n%s", acl)
	}
	if got := maskOf(t, filepath.Join(private, "b.txt")); got != maskBefore {
		t.Errorf("revocation changed the file mask from %q to %q", maskBefore, got)
	}
	if again, err := restarted.ExpireViewing(ctx); err != nil || len(again) != 0 {
		t.Errorf("second ExpireViewing() = %v, %v", again, err)
	}
	if _, err := os.Stat("/var/lib/a-nas/viewing-grants.json"); err != nil {
		t.Fatal(err)
	}
}

func maskOf(t *testing.T, path string) string {
	t.Helper()
	acl := asUser("root", "getfacl", "--omit-header", "--absolute-names", path).output
	return regexp.MustCompile(`(?m)^mask::(\S+)`).FindString(acl)
}

func outcomeOf(err error) outcome {
	if err != nil {
		return outcome{err.Error(), false}
	}
	return outcome{"", true}
}
