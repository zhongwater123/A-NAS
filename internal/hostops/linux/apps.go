package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/appid"
)

// Apps installed from the App Center run as their own Linux identity
// app-<id> (ADR 0008). Their data lives on the data volume in apps/<id>,
// which SMB does not publish; the only other folders they may mount are in
// the Shared folder.

const appsDirectory = "apps"

// AppIdentity returns the Linux identity of an app, reserving its UID on first
// use; PrepareApp creates the account, so showing a plan changes nothing on
// the host but the registry. UIDs come from the app range and are never
// reused.
func (e *Executor) AppIdentity(ctx context.Context, appID string) (appid.Identity, error) {
	if e.appsError != nil {
		return appid.Identity{}, e.appsError
	}
	username := appid.Username(appID)
	if !appid.Valid(appID) || !validUsername(username) {
		return appid.Identity{}, errors.New("app ID cannot name a Linux account")
	}
	e.appsMu.Lock()
	defer e.appsMu.Unlock()
	uid, known := e.apps[appID]
	if !known {
		uid = appid.FirstUID
		for _, used := range e.apps {
			if used >= uid {
				uid = used + 1
			}
		}
		if uid > appid.LastUID {
			return appid.Identity{}, errors.New("A-NAS app identity range is exhausted")
		}
	}
	if _, _, err := e.accountState(ctx, username, uid); err != nil {
		return appid.Identity{}, err
	}
	if !known {
		e.apps[appID] = uid
		if err := e.saveAppsLocked(); err != nil {
			return appid.Identity{}, err
		}
	}
	return appid.Identity{Username: username, UID: uid, GID: uid}, nil
}

// PrepareApp creates the folders an app mounts before Docker starts it, so
// Docker never creates a host folder, and gives the app access to the Shared
// folder only when its plan mounts from there. App-data folders belong to the
// app; Shared folders inherit the Shared ACL.
func (e *Executor) PrepareApp(ctx context.Context, appID string, folders []string, shared bool) error {
	if !appid.Valid(appID) {
		return errors.New("invalid app ID")
	}
	appRoot := filepath.Join(e.mountPoint, appsDirectory, appID)
	sharedRoot := filepath.Join(e.mountPoint, "spaces", "shared")
	for _, folder := range folders {
		clean := filepath.Clean(folder)
		if clean != folder || !filepath.IsAbs(folder) {
			return fmt.Errorf("app folder %s is not a clean absolute path", folder)
		}
		switch {
		case clean == appRoot || withinDirectory(appRoot, clean):
		case shared && (clean == sharedRoot || withinDirectory(sharedRoot, clean)):
		default:
			return fmt.Errorf("app folder %s is outside the app's data and the Shared folder", folder)
		}
	}
	if !e.dataVolumeReady() {
		return errors.New("the data volume is not available")
	}
	identity, err := e.AppIdentity(ctx, appID)
	if err != nil {
		return err
	}
	if err := e.ensureAccount(ctx, identity.Username, identity.UID); err != nil {
		return err
	}
	if err := e.ensureAppsSubvolume(ctx); err != nil {
		return err
	}
	owner := fmt.Sprintf("%d:%d", identity.UID, identity.GID)
	// The app can rename entries in its data and, like members, in Shared:
	// every folder is created and owned through descriptors that do not
	// follow links (Docker mounts them as subpaths for the same reason).
	if err := e.makeFolder(ctx, appRoot, 0o750, owner); err != nil {
		return err
	}
	for _, folder := range folders {
		switch {
		case withinDirectory(appRoot, folder) && folder != appRoot:
			if err := e.makeFolders(ctx, appRoot, folder, 0o750, owner); err != nil {
				return err
			}
		case withinDirectory(sharedRoot, folder) && folder != sharedRoot:
			// Created by root, a Shared folder inherits the Shared default ACL;
			// the mode keeps its mask from masking the inherited entries.
			if err := e.makeFolders(ctx, sharedRoot, folder, 0o770, ""); err != nil {
				return fmt.Errorf("create Shared folder for app: %w", err)
			}
		}
	}
	return e.setAppSharedAccess(ctx, identity.Username, shared)
}

// ReleaseApp withdraws an uninstalled app's Shared folder access. The
// identity and its files stay so a reinstall finds its data.
func (e *Executor) ReleaseApp(ctx context.Context, appID string) error {
	if !appid.Valid(appID) {
		return errors.New("invalid app ID")
	}
	e.appsMu.Lock()
	_, known := e.apps[appID]
	e.appsMu.Unlock()
	if !known {
		return nil
	}
	return e.setAppSharedAccess(ctx, appid.Username(appID), false)
}

// setAppSharedAccess grants or withdraws the Shared folder ACL, which
// a-nas-users holds, by group membership.
func (e *Executor) setAppSharedAccess(ctx context.Context, username string, shared bool) error {
	groups := ""
	if shared {
		if err := e.ensureFixedGroups(ctx); err != nil {
			return err
		}
		groups = accounts.UsersGroup
	}
	if output, err := e.runner.Run(ctx, "usermod", []string{"--groups", groups, username}, ""); err != nil {
		return commandError("set app Shared access", err, output)
	}
	return nil
}

func (e *Executor) ensureAppsSubvolume(ctx context.Context) error {
	apps := filepath.Join(e.mountPoint, appsDirectory)
	if info, err := os.Lstat(apps); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("apps folder is not a safe directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if output, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "create", apps}, ""); err != nil {
			return commandError("create apps subvolume", err, output)
		}
	} else {
		return err
	}
	// Only root and Docker reach app data; containers see their own folder.
	_, _, err := e.ensureDirectoryACL(ctx, apps, strings.Join([]string{"user::rwx", "group::---", "other::---"}, ","), false)
	return err
}

// makeFolder opens or creates one folder without following links and, when
// owner is set, gives it to the app.
func (e *Executor) makeFolder(ctx context.Context, path string, mode uint32, owner string) error {
	folder, _, err := e.openVolumeDirectory(path, mode, false)
	if err != nil {
		return err
	}
	defer folder.Close()
	if owner == "" {
		return nil
	}
	if output, err := e.runner.Run(ctx, "chown", []string{owner, openedPath(folder)}, ""); err != nil {
		return commandError("own app folder", err, output)
	}
	return nil
}

// makeFolders makes each folder between root (exclusive) and path.
func (e *Executor) makeFolders(ctx context.Context, root, path string, mode uint32, owner string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := e.makeFolder(ctx, current, mode, owner); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) loadAppsRegistry() error {
	contents, err := os.ReadFile(e.appsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(contents, &e.apps); err != nil {
		return fmt.Errorf("decode app identity registry: %w", err)
	}
	for id, uid := range e.apps {
		if !appid.Valid(id) || uid < appid.FirstUID || uid > appid.LastUID {
			return errors.New("app identity registry contains an invalid entry")
		}
	}
	return nil
}

func (e *Executor) saveAppsLocked() error {
	contents, err := json.MarshalIndent(e.apps, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(e.appsPath, append(contents, '\n'), 0o600)
}
