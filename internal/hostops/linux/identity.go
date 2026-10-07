package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// identityRecord is what the Host Agent remembers about an A-NAS Linux
// identity. The registry is mirrored onto the data volume so a reinstalled
// system disk can recreate accounts with the UIDs that own the files.
type identityRecord struct {
	UID     int           `json:"uid"`
	Role    accounts.Role `json:"role"`
	Enabled bool          `json:"enabled"`
}

const identityManifestName = ".a-nas-identities.json"

var fixedGroups = []struct {
	name string
	gid  int
}{
	{name: accounts.UsersGroup, gid: accounts.UsersGID},
	{name: accounts.AdminsGroup, gid: accounts.AdminsGID},
}

// SyncIdentities converges host accounts and group membership without
// touching passwords. Disabled identities keep their account so files stay
// attributed, but their SMB credential is disabled.
func (e *Executor) SyncIdentities(ctx context.Context, identities []accounts.Identity) error {
	if e.identityError != nil {
		return e.identityError
	}
	for _, identity := range identities {
		if err := validIdentity(identity); err != nil {
			return err
		}
	}
	if err := e.ensureFixedGroups(ctx); err != nil {
		return err
	}
	// One conflicting account must not block the others from converging.
	var failures []error
	for _, identity := range identities {
		if err := e.syncIdentity(ctx, identity); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", identity.Username, err))
		}
	}
	return errors.Join(append(failures, e.mirrorIdentityManifest())...)
}

func (e *Executor) syncIdentity(ctx context.Context, identity accounts.Identity) error {
	if err := e.ensureIdentity(ctx, identity); err != nil {
		return err
	}
	if !identity.Enabled {
		if err := e.disableSambaIfPresent(ctx, identity.Username); err != nil {
			return err
		}
	}
	return e.recordIdentity(identity)
}

func validIdentity(identity accounts.Identity) error {
	if !validUsername(identity.Username) {
		return errors.New("invalid identity username")
	}
	if identity.UID < accounts.FirstUserUID || identity.UID > accounts.LastUserUID {
		return errors.New("identity UID is outside the A-NAS range")
	}
	if identity.Role != accounts.RoleAdmin && identity.Role != accounts.RoleMember {
		return errors.New("invalid identity role")
	}
	return nil
}

func (e *Executor) ensureFixedGroups(ctx context.Context) error {
	for _, group := range fixedGroups {
		fields, found, err := e.getent(ctx, "group", group.name)
		if err != nil {
			return err
		}
		if found {
			if len(fields) < 3 || fields[2] != strconv.Itoa(group.gid) {
				return fmt.Errorf("host group %s exists with an unexpected GID", group.name)
			}
			continue
		}
		if output, err := e.runner.Run(ctx, "groupadd", []string{"--gid", strconv.Itoa(group.gid), group.name}, ""); err != nil {
			return commandError("create A-NAS group", err, output)
		}
	}
	return nil
}

// ensureIdentity creates or adopts the Linux account for one A-NAS user. It
// never modifies an account, group, or UID that A-NAS did not allocate.
func (e *Executor) ensureIdentity(ctx context.Context, identity accounts.Identity) error {
	uid := strconv.Itoa(identity.UID)
	fields, found, err := e.getent(ctx, "passwd", identity.Username)
	if err != nil {
		return err
	}
	if found {
		if len(fields) < 4 || fields[2] != uid || fields[3] != uid {
			return fmt.Errorf("%w: %s", accounts.ErrIdentityConflict, identity.Username)
		}
	} else {
		if _, taken, err := e.getent(ctx, "passwd", uid); err != nil {
			return err
		} else if taken {
			return fmt.Errorf("%w: UID %s", accounts.ErrIdentityConflict, uid)
		}
		group, groupFound, err := e.getent(ctx, "group", identity.Username)
		if err != nil {
			return err
		}
		if groupFound && (len(group) < 3 || group[2] != uid) {
			return fmt.Errorf("%w: group %s", accounts.ErrIdentityConflict, identity.Username)
		}
		if !groupFound {
			if _, taken, err := e.getent(ctx, "group", uid); err != nil {
				return err
			} else if taken {
				return fmt.Errorf("%w: GID %s", accounts.ErrIdentityConflict, uid)
			}
			if output, err := e.runner.Run(ctx, "groupadd", []string{"--gid", uid, identity.Username}, ""); err != nil {
				return commandError("create identity group", err, output)
			}
		}
		if output, err := e.runner.Run(ctx, "useradd", []string{
			"--uid", uid, "--gid", uid, "--no-create-home", "--home-dir", "/nonexistent",
			"--shell", "/usr/sbin/nologin", identity.Username,
		}, ""); err != nil {
			return commandError("create A-NAS identity", err, output)
		}
	}
	groups := accounts.UsersGroup
	if identity.Role == accounts.RoleAdmin {
		groups += "," + accounts.AdminsGroup
	}
	if output, err := e.runner.Run(ctx, "usermod", []string{"--groups", groups, identity.Username}, ""); err != nil {
		return commandError("set identity groups", err, output)
	}
	return nil
}

type exitCoder interface{ ExitCode() int }

// getent returns the colon-separated fields of one passwd or group entry.
// Exit status 2 means the key does not exist.
func (e *Executor) getent(ctx context.Context, database, key string) ([]string, bool, error) {
	output, err := e.runner.Run(ctx, "getent", []string{database, key}, "")
	if err != nil {
		var coded exitCoder
		if errors.As(err, &coded) && coded.ExitCode() == 2 {
			return nil, false, nil
		}
		return nil, false, commandError("look up host "+database, err, output)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	if line == "" {
		return nil, false, nil
	}
	return strings.Split(line, ":"), true, nil
}

func (e *Executor) disableSambaIfPresent(ctx context.Context, username string) error {
	if _, err := e.runner.Run(ctx, "pdbedit", []string{"--user", username}, ""); err != nil {
		var coded exitCoder
		if errors.As(err, &coded) {
			return nil
		}
		return fmt.Errorf("look up Samba account: %w", err)
	}
	if output, err := e.runner.Run(ctx, "smbpasswd", []string{"-d", username}, ""); err != nil {
		return commandError("disable Samba credential", err, output)
	}
	return e.disconnectSMBSessions(ctx, username)
}

var smbPID = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)

// disconnectSMBSessions ends the smbd processes serving an account, so a
// disabled user loses established sessions instead of keeping them until the
// client disconnects.
func (e *Executor) disconnectSMBSessions(ctx context.Context, username string) error {
	output, err := e.runner.Run(ctx, "smbstatus", []string{"--processes", "--user=" + username, "--json"}, "")
	if err != nil {
		return commandError("list SMB sessions", err, output)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil
	}
	var status struct {
		Sessions map[string]struct {
			Username string `json:"username"`
			ServerID struct {
				PID json.Number `json:"pid"`
			} `json:"server_id"`
		} `json:"sessions"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	decoder.UseNumber()
	if err := decoder.Decode(&status); err != nil {
		return fmt.Errorf("decode SMB sessions: %w", err)
	}
	var pids []string
	for _, session := range status.Sessions {
		pid := session.ServerID.PID.String()
		if !strings.EqualFold(session.Username, username) || !smbPID.MatchString(pid) || slices.Contains(pids, pid) {
			continue
		}
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	for _, pid := range pids {
		if output, err := e.runner.Run(ctx, "smbcontrol", []string{pid, "shutdown"}, ""); err != nil {
			return commandError("disconnect SMB session", err, output)
		}
	}
	return nil
}

func (e *Executor) loadIdentityRegistry() error {
	contents, err := os.ReadFile(e.identityPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var records map[string]identityRecord
	if err := json.Unmarshal(contents, &records); err != nil {
		return fmt.Errorf("decode identity registry: %w", err)
	}
	for username, record := range records {
		if err := validIdentity(accounts.Identity{Username: username, UID: record.UID, Role: record.Role}); err != nil {
			return fmt.Errorf("identity registry: %w", err)
		}
		e.identities[username] = record
	}
	return nil
}

func (e *Executor) recordIdentity(identity accounts.Identity) error {
	e.identitiesMu.Lock()
	defer e.identitiesMu.Unlock()
	e.identities[identity.Username] = identityRecord{UID: identity.UID, Role: identity.Role, Enabled: identity.Enabled}
	return e.writeIdentityRegistryLocked(e.identityPath)
}

func (e *Executor) setIdentityEnabled(username string, enabled bool) error {
	e.identitiesMu.Lock()
	defer e.identitiesMu.Unlock()
	record, ok := e.identities[username]
	if !ok {
		return nil
	}
	record.Enabled = enabled
	e.identities[username] = record
	return e.writeIdentityRegistryLocked(e.identityPath)
}

func (e *Executor) writeIdentityRegistryLocked(path string) error {
	contents, err := json.MarshalIndent(e.identities, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(contents, '\n'), 0o600)
}

// mirrorIdentityManifest copies the identity registry onto the data volume
// when it is mounted. An offline volume is left untouched.
func (e *Executor) mirrorIdentityManifest() error {
	if !e.dataVolumeReady() {
		return nil
	}
	e.identitiesMu.Lock()
	defer e.identitiesMu.Unlock()
	return e.writeIdentityRegistryLocked(filepath.Join(e.mountPoint, identityManifestName))
}

func (e *Executor) identityUsernames() []string {
	e.identitiesMu.Lock()
	defer e.identitiesMu.Unlock()
	usernames := make([]string, 0, len(e.identities))
	for username := range e.identities {
		usernames = append(usernames, username)
	}
	slices.Sort(usernames)
	return usernames
}

var _ accounts.IdentitySynchronizer = (*Executor)(nil)
