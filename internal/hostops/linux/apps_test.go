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

func TestAppIdentitiesComeFromTheAppRangeAndAreNeverReused(t *testing.T) {
	host := newFakeHost()
	systemRoot := t.TempDir()
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: systemRoot, MountPoint: filepath.Join(t.TempDir(), "data")})
	ctx := context.Background()

	memos, err := executor.AppIdentity(ctx, "memos")
	if err != nil || memos.Username != "app-memos" || memos.UID != 30000 || memos.GID != 30000 {
		t.Fatalf("AppIdentity(memos) = %+v, %v", memos, err)
	}
	if !host.ran("useradd --uid 30000 --gid 30000 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin app-memos") {
		t.Fatalf("app account not created:\n%s", strings.Join(host.commands, "\n"))
	}
	again, err := executor.AppIdentity(ctx, "memos")
	if err != nil || again != memos {
		t.Fatalf("second AppIdentity(memos) = %+v, %v; want the same identity", again, err)
	}
	restarted := linux.NewExecutor(nil, host, linux.Options{SystemRoot: systemRoot, MountPoint: filepath.Join(t.TempDir(), "data")})
	navidrome, err := restarted.AppIdentity(ctx, "navidrome")
	if err != nil || navidrome.UID != 30001 {
		t.Fatalf("AppIdentity(navidrome) after restart = %+v, %v; want the next unused UID", navidrome, err)
	}

	host.users["app-squatter"] = [2]int{1200, 1200}
	if _, err := restarted.AppIdentity(ctx, "squatter"); !errors.Is(err, accounts.ErrIdentityConflict) {
		t.Fatalf("AppIdentity over an existing account error = %v, want ErrIdentityConflict", err)
	}
	if _, err := restarted.AppIdentity(ctx, "Not Valid"); err == nil {
		t.Fatal("AppIdentity accepted an invalid app ID")
	}
}

func TestPrepareAppOnlyCreatesTheAppsFoldersAndApprovedSharedFolders(t *testing.T) {
	host := newFakeHost()
	mount := filepath.Join(t.TempDir(), "data")
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: t.TempDir(), MountPoint: mount})
	ctx := context.Background()
	for name, test := range map[string]struct {
		folders []string
		shared  bool
	}{
		"volume root":            {[]string{mount}, true},
		"another app":            {[]string{filepath.Join(mount, "apps", "other", "data")}, true},
		"private space":          {[]string{filepath.Join(mount, "spaces", "private", "alice")}, true},
		"escape":                 {[]string{filepath.Join(mount, "apps", "memos") + "/../other"}, true},
		"relative":               {[]string{"apps/memos"}, true},
		"Shared without consent": {[]string{filepath.Join(mount, "spaces", "shared", "Media")}, false},
		"system disk":            {[]string{"/etc"}, true},
	} {
		if err := executor.PrepareApp(ctx, "memos", test.folders, test.shared); err == nil || !strings.Contains(err.Error(), "app folder") {
			t.Errorf("%s: PrepareApp() error = %v, want a refused folder", name, err)
		}
	}
	if _, err := os.Stat(mount); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused preparation created %s", mount)
	}
	err := executor.PrepareApp(ctx, "memos", []string{filepath.Join(mount, "apps", "memos", "data")}, false)
	if err == nil || !strings.Contains(err.Error(), "data volume") {
		t.Fatalf("PrepareApp() on an offline volume error = %v", err)
	}
}
