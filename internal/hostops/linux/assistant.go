package linux

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/assistantkey"
)

// The AI assistant (ADR 0024) is on while the administrator's DeepSeek key is saved
// (docs/decisions/2026-11-17-assistant-v1-cloud-model.md). The Host Agent keeps the key as
// the systemd credential below, which only the model gateway's unit loads, and prepares every
// active member's assistant: the subvolume on the data volume, the instance's secret and the
// member's Hermes settings in its own state directory, and the units that start it. Deleting
// the key turns every assistant off and keeps what they hold.
const (
	assistantCredential  = "a-nas-assistant-deepseek"
	assistantGatewayUnit = "anas-assistant-gateway.service"
	assistantWakerUnit   = "anas-assistant-cron-waker.service"
	// assistantPlannerUnit hands VideoCut's planning to members'
	// planning instances (docs/architecture/montage-planning-session.md).
	assistantPlannerUnit = "anas-videocut-planner.service"
)

// assistantTemplates are the assistant's unit templates (ADR 0024).
var assistantTemplates = []string{
	"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-api@.socket",
	"anas-assistant-cron@.service", "anas-assistant-curator@.service", "anas-assistant-curator@.timer",
	"anas-assistant-plan@.service", "anas-assistant-planapi@.service", "anas-assistant-planapi@.socket",
	// The member's private network with its bridge to the model, which the
	// units above need: without them it would stop by itself, but only
	// after this returns, and it still runs as the member.
	"anas-assistant-net@.service",
}

// assistantEntries are the units that start a member's assistant: the API
// socket the File Broker connects to, the planning instance's socket root's
// planner connects to, and the weekly curator timer. The others run only when
// these start them.
var assistantEntries = []string{"anas-assistant-api@.socket", "anas-assistant-planapi@.socket", "anas-assistant-curator@.timer"}

// assistantServices are what runs of a member's assistant, which deleting the key stops.
var assistantServices = []string{
	"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-cron@.service",
	"anas-assistant-curator@.service", "anas-assistant-plan@.service", "anas-assistant-planapi@.service", "anas-assistant-net@.service",
}

// The places a member's sandbox binds over the Hermes home (anas-assistant@.service): Hermes
// loads code and settings from them, so each must be there, of its kind, before it starts.
var (
	hermesMountFiles   = []string{"config.yaml", ".env", "SOUL.md", "active_profile"}
	hermesMountFolders = []string{"hooks", "plugins", "profiles"}
)

// Hermes's own login (plugins/dashboard_auth/basic) takes the scrypt hash of the instance's
// secret in the form its hash_password documents for config.yaml:
// scrypt$N$r$p$<salt, base64>$<key, base64>.
const (
	hermesScryptN    = 1 << 14
	hermesScryptR    = 8
	hermesScryptP    = 1
	hermesScryptLen  = 32
	hermesSaltLength = 16
	assistantSecret  = "HERMES_DASHBOARD_SESSION_TOKEN="
)

var assistantSecretPattern = regexp.MustCompile(`^` + assistantSecret + `[0-9a-f]{64}\n?$`)

// hermesSettings is the member's config.yaml: the skill folders, the managed policy's MCP
// servers again (serve's discovery reads the raw user config) and Hermes's login.
type hermesSettings struct {
	Skills struct {
		ExternalDirs []string `yaml:"external_dirs,flow"`
	} `yaml:"skills"`
	MCPServers map[string]any `yaml:"mcp_servers,omitempty"`
	Dashboard  struct {
		BasicAuth struct {
			Username     string `yaml:"username"`
			PasswordHash string `yaml:"password_hash"`
		} `yaml:"basic_auth"`
	} `yaml:"dashboard"`
}

// assistantUnits names username's instances of the installed templates among
// templates; none when the assistant is not installed.
func (e *Executor) assistantUnits(username string, templates []string) ([]string, error) {
	var units []string
	for _, template := range templates {
		installed := false
		for _, directory := range []string{filepath.Join("usr", "lib", "systemd", "system"), filepath.Join("etc", "systemd", "system")} {
			if _, err := os.Stat(filepath.Join(e.systemRoot, directory, template)); err == nil {
				installed = true
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("inspect assistant installation: %w", err)
			}
		}
		if installed {
			units = append(units, strings.Replace(template, "@.", "@"+username+".", 1))
		}
	}
	return units, nil
}

// assistantState is the Host Agent's own record of the members' assistants.
func (e *Executor) assistantState() string {
	return filepath.Join(e.systemRoot, "var", "lib", stateDirectoryName, "assistant")
}

// AssistantTokenDir holds the members' instance secrets: each the password of Hermes's login,
// with which the File Broker signs in for the member's live session (decision 10).
func (e *Executor) AssistantTokenDir() string {
	return filepath.Join(e.assistantState(), "tokens")
}

// assistantTokenPath is the member's instance secret in AssistantTokenDir.
func (e *Executor) assistantTokenPath(username string) string {
	return filepath.Join(e.AssistantTokenDir(), username+".env")
}

// AssistantPlanDir holds the members' planning grants for VideoCut, one file each
// named by the account: the File Broker writes them behind the member's live session, and
// root's planner reads them (ai/assistant/videocut-planner.py). It is in the Host Agent's own state
// directory, which no service can write to (docs/architecture/montage-center.md, 规划授权).
func (e *Executor) AssistantPlanDir() string {
	return filepath.Join(e.assistantState(), "plans")
}

// forgetPlanGrant removes the planning grant of an account that is being closed: a grant is
// the member's own, and the name may be another member's later.
func (e *Executor) forgetPlanGrant(username string) error {
	if !accountid.ValidAssistantAccount(username) {
		return nil
	}
	if err := os.Remove(filepath.Join(e.AssistantPlanDir(), username)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s's planning grant: %w", username, err)
	}
	return nil
}

// assistantMemberDir holds the member's Hermes settings and skill, which their sandbox binds.
func (e *Executor) assistantMemberDir(username string) string {
	return filepath.Join(e.assistantState(), "members", username)
}

func (e *Executor) assistantCredentialPath() string {
	return filepath.Join(e.systemRoot, "etc", "credstore.encrypted", assistantCredential)
}

// assistantLibrary is where the a-nas package installs the assistant's files.
func (e *Executor) assistantLibrary() string {
	return filepath.Join(e.systemRoot, "usr", "lib", "a-nas", "assistant")
}

// assistantKeySaved reports whether the administrator's key is saved.
func (e *Executor) assistantKeySaved() (bool, error) {
	info, err := os.Lstat(e.assistantCredentialPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect the assistant's key: %w", err)
	}
	return info.Mode().IsRegular(), nil
}

// AssistantKey reports whether the key is saved and what systemd says of the gateway.
func (e *Executor) AssistantKey(ctx context.Context) (assistantkey.State, error) {
	saved, err := e.assistantKeySaved()
	if err != nil {
		return assistantkey.State{}, err
	}
	state := assistantkey.State{Configured: saved}
	output, err := e.runner.Output(ctx, "systemctl", []string{"show", "--property=ActiveState", "--value", assistantGatewayUnit}, "")
	if err != nil {
		return assistantkey.State{}, commandError("read the model gateway's state", err, output)
	}
	switch strings.TrimSpace(string(output)) {
	case "active", "reloading":
		state.Gateway = assistantkey.Running
	case "activating":
		state.Gateway = assistantkey.Starting
	case "failed":
		state.Gateway = assistantkey.Failed
	default:
		state.Gateway = assistantkey.Stopped
	}
	return state, nil
}

// SetAssistantKey saves the administrator's key, encrypted with the machine's credential key,
// restarts the gateway with it and prepares the assistant of every active member. A member
// who cannot be prepared does not keep the others from theirs.
func (e *Executor) SetAssistantKey(ctx context.Context, key string) (assistantkey.State, error) {
	if !assistantkey.ValidKey(key) {
		return assistantkey.State{}, errors.New("invalid assistant key")
	}
	e.assistantMu.Lock()
	defer e.assistantMu.Unlock()
	path := e.assistantCredentialPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return assistantkey.State{}, fmt.Errorf("prepare the credential store: %w", err)
	}
	// The key reaches systemd-creds on its standard input, never on a command line, and the
	// credential comes back on its standard output. The machine's own credential key
	// (/var/lib/systemd/credential.secret, which anas-site.service makes on the machine itself)
	// encrypts it; a TPM would tie it to the firmware's state too, and a firmware update would
	// then lose it.
	encrypted, err := e.runner.Output(ctx, "systemd-creds", []string{"encrypt", "--name=" + assistantCredential, "--with-key=host", "-", "-"}, key)
	if err != nil {
		return assistantkey.State{}, fmt.Errorf("encrypt the assistant's key: %w", err)
	}
	if len(encrypted) == 0 {
		return assistantkey.State{}, errors.New("encrypt the assistant's key: systemd-creds wrote nothing")
	}
	// Whole on disk before the gateway restarts with it, also across a power cut.
	if err := writeAtomic(path, encrypted, 0o600); err != nil {
		return assistantkey.State{}, fmt.Errorf("save the assistant's key: %w", err)
	}
	var failures []error
	for _, command := range [][]string{{"restart", "--no-block", assistantGatewayUnit}, {"start", "--no-block", assistantWakerUnit},
		{"start", "--no-block", assistantPlannerUnit}} {
		if output, err := e.runner.Run(ctx, "systemctl", command, ""); err != nil {
			failures = append(failures, commandError(command[0]+" "+command[2], err, output))
		}
	}
	for _, member := range e.activeMembers() {
		if err := e.prepareAssistant(ctx, member.username, member.uid); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", member.username, err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		e.logger.Error("the assistant's key is saved, but not every assistant is ready", "error", err)
	}
	return e.AssistantKey(ctx)
}

// DeleteAssistantKey deletes the key and turns every member's assistant off: its entry
// points no longer start it and what runs stops. Their folders, conversations, memory,
// scheduled tasks and secrets stay, so saving a key again brings each back as it was.
func (e *Executor) DeleteAssistantKey(ctx context.Context) (assistantkey.State, error) {
	e.assistantMu.Lock()
	defer e.assistantMu.Unlock()
	if err := os.Remove(e.assistantCredentialPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return assistantkey.State{}, fmt.Errorf("delete the assistant's key: %w", err)
	}
	var failures []error
	if output, err := e.systemctlWithoutKey(ctx, "stop", assistantWakerUnit, assistantPlannerUnit, assistantGatewayUnit); err != nil {
		failures = append(failures, commandError("stop the model gateway", err, output))
	}
	// A disabled member's units are masked and stopped already (disableAssistant).
	for _, member := range e.activeMembers() {
		entries, err := e.assistantUnits(member.username, assistantEntries)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		services, err := e.assistantUnits(member.username, assistantServices)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if len(entries) > 0 {
			if output, err := e.systemctlWithoutKey(ctx, append([]string{"disable", "--now", "--quiet"}, entries...)...); err != nil {
				failures = append(failures, commandError("turn off "+member.username+"'s assistant", err, output))
			}
		}
		if len(services) > 0 {
			if output, err := e.systemctlWithoutKey(ctx, append([]string{"stop"}, services...)...); err != nil {
				failures = append(failures, commandError("stop "+member.username+"'s assistant", err, output))
			}
		}
	}
	if err := errors.Join(failures...); err != nil {
		return assistantkey.State{}, err
	}
	return e.AssistantKey(ctx)
}

// systemctlWithoutKey runs systemctl on units that need the key, once it is deleted. A start
// queued while their stop job runs (the File Broker's 立即运行, a socket the Product Service
// still connects to) replaces that job, and systemctl then fails with "Job for … canceled".
// Without the key such a start runs nothing (the units' ConditionPathExists=), so the same
// command, run once more, finds them stopped.
func (e *Executor) systemctlWithoutKey(ctx context.Context, args ...string) ([]byte, error) {
	output, err := e.runner.Run(ctx, "systemctl", args, "")
	if err != nil {
		output, err = e.runner.Run(ctx, "systemctl", args, "")
	}
	return output, err
}

// AssistantOn reports whether the administrator's key is saved, which every member's
// assistant needs: the File Broker starts no round of scheduled tasks without it.
func (e *Executor) AssistantOn() bool {
	saved, err := e.assistantKeySaved()
	return err == nil && saved
}

// disableAssistant stops every way an assistant can run for a disabled member.
//
// Starts keep arriving while it runs: the Product Service keeps a signed-in
// member's instance (its socket starts the instance again), the planner connects to
// the planning instance's socket, and the waker starts due tasks. In systemd a start
// queued after a stop job replaces it, and systemctl then fails with "Job for …
// canceled" while the unit runs on.
// So the units are masked before anything is stopped: from then on systemd
// refuses those starts, and the stop jobs complete. Disabling, which starts no
// jobs, comes first: systemctl leaves the boot and timer links of a unit that
// is already masked.
//
// It does not wait for assistantMu, which saving the key holds while it prepares every
// member: a mask wins in either order against preparing the member's assistant, whose
// enabling a mask refuses, so an account is closed at once.
func (e *Executor) disableAssistant(ctx context.Context, username string) error {
	// The member's planning grant goes first, whatever becomes of the units: without it the
	// planner hands no round of planning to the account.
	if err := e.forgetPlanGrant(username); err != nil {
		return err
	}
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
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return e.removeStaleAssistantSocket(username)
}

// assistantSocketPath is the member's API socket, which anas-assistant-api@.socket listens on.
func (e *Executor) assistantSocketPath(username string) string {
	return filepath.Join(e.systemRoot, "run", "a-nas-assistant", username+".sock")
}

// assistantPlanSocketPath is the socket of the member's planning instance, which
// anas-assistant-planapi@.socket listens on.
func (e *Executor) assistantPlanSocketPath(username string) string {
	return filepath.Join(e.systemRoot, "run", "a-nas-assistant-plan", username+".sock")
}

// removeStaleAssistantSocket deletes the member's API socket and that of the planning
// instance once their units are masked and stopped. Masking a running socket unit replaces
// its configuration, so its RemoveOnStop= no longer applies when the mask stops it, and the
// file would stay: the Product Service would then report an assistant that nothing listens
// for, and a name used again would find it.
func (e *Executor) removeStaleAssistantSocket(username string) error {
	for _, path := range []string{e.assistantSocketPath(username), e.assistantPlanSocketPath(username)} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s's assistant socket: %w", username, err)
		}
		if info.Mode().Type() != os.ModeSocket {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s's assistant socket: %w", username, err)
		}
	}
	return nil
}

// enableAssistant undoes disableAssistant for the active member username, whose account is
// uid: it unmasks the units and, while the administrator's key is saved, prepares the
// member's assistant and enables its entry points. Without the key the member gets them when
// it is saved. The assistant is not part of the account: one that cannot be prepared is
// logged and tried again with the next synchronization, the next saving of the key or the
// data volume's reconciliation, and the account is enabled all the same.
func (e *Executor) enableAssistant(ctx context.Context, username string, uid int) error {
	e.assistantMu.Lock()
	defer e.assistantMu.Unlock()
	units, err := e.assistantUnits(username, assistantTemplates)
	if err != nil || len(units) == 0 {
		return err
	}
	if output, err := e.runner.Run(ctx, "systemctl", append([]string{"unmask"}, units...), ""); err != nil {
		return commandError("unmask assistant units", err, output)
	}
	if err := e.prepareAssistant(ctx, username, uid); err != nil {
		e.logger.ErrorContext(ctx, "the member's assistant is not ready", "username", username, "error", err)
	}
	return nil
}

// PrepareAssistants prepares, while the key is saved, the assistant of every active member
// whose folders the data volume lacks: one prepared while the volume was not mounted, or a
// volume made or mounted since. The Host Agent's reconciliation of the data volume calls it.
func (e *Executor) PrepareAssistants(ctx context.Context) error {
	if !e.dataVolumeReady() {
		return nil
	}
	e.assistantMu.Lock()
	defer e.assistantMu.Unlock()
	if saved, err := e.assistantKeySaved(); err != nil || !saved {
		return err
	}
	var failures []error
	for _, member := range e.activeMembers() {
		if _, err := os.Lstat(filepath.Join(e.mountPoint, "assistant", member.username, "hermes")); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := e.prepareAssistant(ctx, member.username, member.uid); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", member.username, err))
		}
	}
	return errors.Join(failures...)
}

// assistantMember is an account the identity registry holds as enabled.
type assistantMember struct {
	username string
	uid      int
}

// activeMembers are the accounts the identity registry holds as enabled, by name.
func (e *Executor) activeMembers() []assistantMember {
	e.identitiesMu.Lock()
	defer e.identitiesMu.Unlock()
	var members []assistantMember
	for username, record := range e.identities {
		if record.Enabled {
			members = append(members, assistantMember{username: username, uid: record.UID})
		}
	}
	slices.SortFunc(members, func(a, b assistantMember) int { return strings.Compare(a.username, b.username) })
	return members
}

func (e *Executor) identityUID(username string) (int, bool) {
	e.identitiesMu.Lock()
	defer e.identitiesMu.Unlock()
	record, ok := e.identities[username]
	return record.UID, ok
}

// prepareAssistant makes the assistant of username, whose account is uid, ready to start and
// enables its entry points, while the key is saved; the caller holds assistantMu. Each step
// is the same when it is done already.
func (e *Executor) prepareAssistant(ctx context.Context, username string, uid int) error {
	saved, err := e.assistantKeySaved()
	if err != nil || !saved {
		return err
	}
	entries, err := e.assistantUnits(username, assistantEntries)
	if err != nil || len(entries) == 0 {
		return err
	}
	if err := e.prepareAssistantSettings(username); err != nil {
		return err
	}
	// Without the volume the sandbox has nothing to bind and cannot start; the reconciliation
	// of the data volume makes its folders once it is mounted (PrepareAssistants).
	if e.dataVolumeReady() {
		if err := e.prepareAssistantFolders(ctx, username, uid); err != nil {
			return err
		}
	}
	if output, err := e.runner.Run(ctx, "systemctl", append([]string{"enable", "--now", "--quiet"}, entries...), ""); err != nil {
		return commandError("enable assistant units", err, output)
	}
	return nil
}

// prepareAssistantFolders makes the member's assistant directory on the data volume: a
// subvolume of its own that only root writes and the member passes through, its workspace
// files/, which the member writes, and Hermes's home hermes/, which is the member's own.
func (e *Executor) prepareAssistantFolders(ctx context.Context, username string, uid int) error {
	assistants := filepath.Join(e.mountPoint, "assistant")
	if _, _, err := e.ensureDirectoryACL(ctx, assistants, containerACL(), ordinaryDirectory); err != nil {
		return err
	}
	home := filepath.Join(assistants, username)
	if _, err := e.ensureSpaceSubvolume(ctx, home); err != nil {
		return err
	}
	homeACL := strings.Join([]string{"user::rwx", "group::---", "other::---", "user:" + username + ":--x", "mask::--x"}, ",")
	if _, _, err := e.ensureDirectoryACL(ctx, home, homeACL, ordinaryDirectory); err != nil {
		return err
	}
	if _, _, err := e.ensureDirectoryACL(ctx, filepath.Join(home, "files"), inheritedACL("user:"+username+":rwx"), ordinaryDirectory); err != nil {
		return err
	}
	return e.prepareHermesHome(ctx, filepath.Join(home, "hermes"), uid)
}

// prepareHermesHome makes Hermes's home the member's own directory, 0700, without an ACL
// (ADR 0024 decision 3): Hermes's gateway, which runs the scheduled tasks, serves only a home
// that the user it runs as owns and that no ACL lets anyone else write. Its parent is root's
// and the member can only pass through it, so the name cannot be turned into a link; inside,
// where the member writes, every step works on the opened directory and never follows a link.
func (e *Executor) prepareHermesHome(ctx context.Context, path string, uid int) error {
	home, _, err := e.openVolumeDirectory(path, 0o700, ordinaryDirectory)
	if err != nil {
		return err
	}
	defer func() { _ = home.Close() }()
	fd := int(home.Fd())
	if err := unix.Fchown(fd, uid, uid); err != nil {
		return fmt.Errorf("give %s to its member: %w", path, err)
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return fmt.Errorf("protect %s: %w", path, err)
	}
	if output, err := e.runner.Run(ctx, "setfacl", []string{"--remove-all", "--remove-default", openedPath(home)}, ""); err != nil {
		return commandError("protect "+path, err, output)
	}
	for _, name := range hermesMountFiles {
		if err := hermesMountPoint(fd, name, false); err != nil {
			return fmt.Errorf("%s/%s: %w", path, name, err)
		}
	}
	for _, name := range hermesMountFolders {
		if err := hermesMountPoint(fd, name, true); err != nil {
			return fmt.Errorf("%s/%s: %w", path, name, err)
		}
	}
	return nil
}

// hermesMountPoint makes sure name in the directory fd is a file, or a folder, that the unit
// can bind over; anything else there is moved aside.
func hermesMountPoint(fd int, name string, folder bool) error {
	for range 3 {
		var stat unix.Stat_t
		err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case err == nil && folder && stat.Mode&unix.S_IFMT == unix.S_IFDIR:
			return nil
		case err == nil && !folder && stat.Mode&unix.S_IFMT == unix.S_IFREG:
			return nil
		case err == nil:
			aside := fmt.Sprintf("%s.displaced-%d", name, time.Now().UnixNano())
			if err := unix.Renameat2(fd, name, fd, aside, unix.RENAME_NOREPLACE); err != nil && !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("move aside: %w", err)
			}
		case errors.Is(err, unix.ENOENT) && folder:
			if err := unix.Mkdirat(fd, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				return err
			}
		case errors.Is(err, unix.ENOENT):
			created, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
			if err != nil && !errors.Is(err, unix.EEXIST) {
				return err
			}
			if err == nil {
				_ = unix.Close(created)
			}
		default:
			return err
		}
	}
	return errors.New("keeps changing")
}

// prepareAssistantSettings writes what the member's sandbox binds from the Host Agent's
// state: the instance's secret (created once, root's alone), the member's config.yaml, which
// is the only file Hermes reads skills.external_dirs, mcp_servers (under serve) and its login
// from, and the member's own skill. Files are written only when they change, so a running
// assistant keeps what it bound.
func (e *Executor) prepareAssistantSettings(username string) error {
	tokenPath := e.assistantTokenPath(username)
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		return err
	}
	secret, err := os.ReadFile(tokenPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s's instance secret: %w", username, err)
	}
	if !assistantSecretPattern.Match(secret) {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		secret = []byte(assistantSecret + hex.EncodeToString(raw) + "\n")
		if err := writeAtomic(tokenPath, secret, 0o600); err != nil {
			return fmt.Errorf("save %s's instance secret: %w", username, err)
		}
	}
	password := strings.TrimSpace(strings.TrimPrefix(string(secret), assistantSecret))

	library := e.assistantLibrary()
	managed, err := os.ReadFile(filepath.Join(library, "managed", "config.yaml"))
	if err != nil {
		return fmt.Errorf("read the assistant's managed policy: %w", err)
	}
	skill, err := os.ReadFile(filepath.Join(library, "member-skill.md"))
	if err != nil {
		return fmt.Errorf("read the member skill: %w", err)
	}
	member := e.assistantMemberDir(username)
	configPath := filepath.Join(member, "config.yaml")
	// The hash already written stays while it is the secret's: a new salt would rewrite the
	// file, and a running sandbox keeps the copy it bound.
	hash := ""
	if current, err := os.ReadFile(configPath); err == nil {
		var settings hermesSettings
		if yaml.Unmarshal(current, &settings) == nil && hermesHashMatches(settings.Dashboard.BasicAuth.PasswordHash, password) {
			hash = settings.Dashboard.BasicAuth.PasswordHash
		}
	}
	if hash == "" {
		if hash, err = hermesHash(password); err != nil {
			return err
		}
	}
	config, err := memberConfig(username, hash, managed)
	if err != nil {
		return err
	}
	// The planning instance's settings add VideoCut's planning server from its own
	// policy, which no other unit starts (docs/architecture/montage-planning-session.md).
	planConfig := config
	if managedPlan, err := os.ReadFile(filepath.Join(library, "managed-plan", "config.yaml")); err == nil {
		if planConfig, err = memberConfig(username, hash, managedPlan); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read the policy of the assistant's planning instance: %w", err)
	}
	// The scheduled tasks' own settings, which once added that server, are gone.
	if err := os.Remove(filepath.Join(member, "config-cron.yaml")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	skillPath := filepath.Join(member, "skills", "anas-member", "SKILL.md")
	for path, contents := range map[string][]byte{
		configPath: config,
		filepath.Join(member, "config-plan.yaml"): planConfig,
		skillPath: bytes.ReplaceAll(skill, []byte("{{member}}"), []byte(username)),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, contents) {
			continue
		}
		if err := writeAtomic(path, contents, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	// The sandbox binds the member's skills folder and config.yaml; it reads them as the
	// member, so the folders it lists are open to read. The Host Agent's state directory
	// above them stays root's alone.
	for _, path := range []string{member, filepath.Join(member, "skills"), filepath.Join(member, "skills", "anas-member")} {
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// memberConfig is the member's config.yaml (hermesSettings), with the MCP servers of the
// managed policy. The YAML encoder quotes a name such as yes, off or null, which Hermes's
// YAML 1.1 reader would otherwise take for a boolean or nothing.
func memberConfig(username, hash string, managed []byte) ([]byte, error) {
	var policy struct {
		MCPServers map[string]any `yaml:"mcp_servers"`
	}
	if err := yaml.Unmarshal(managed, &policy); err != nil {
		return nil, fmt.Errorf("read the assistant's managed policy: %w", err)
	}
	var settings hermesSettings
	settings.Skills.ExternalDirs = []string{"/usr/lib/a-nas/assistant/skills", "/run/anas-assistant-skills"}
	settings.MCPServers = policy.MCPServers
	settings.Dashboard.BasicAuth.Username = username
	settings.Dashboard.BasicAuth.PasswordHash = hash
	body, err := yaml.Dump(&settings, yaml.WithV4Defaults(), yaml.WithLineWidth(-1))
	if err != nil {
		return nil, fmt.Errorf("write the member's Hermes settings: %w", err)
	}
	return append([]byte("# Written by the Host Agent (ADR 0024); read-only in the member's sandbox.\n"), body...), nil
}

func hermesHash(password string) (string, error) {
	salt := make([]byte, hermesSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := scrypt.Key([]byte(password), salt, hermesScryptN, hermesScryptR, hermesScryptP, hermesScryptLen)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{"scrypt", strconv.Itoa(hermesScryptN), strconv.Itoa(hermesScryptR), strconv.Itoa(hermesScryptP),
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(key)}, "$"), nil
}

// hermesHashMatches checks password against a hash this file wrote; any other form is no match.
func hermesHashMatches(hash, password string) bool {
	fields := strings.Split(hash, "$")
	if len(fields) != 6 || fields[0] != "scrypt" || fields[1] != strconv.Itoa(hermesScryptN) ||
		fields[2] != strconv.Itoa(hermesScryptR) || fields[3] != strconv.Itoa(hermesScryptP) {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(fields[4])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(fields[5])
	if err != nil || len(want) != hermesScryptLen {
		return false
	}
	got, err := scrypt.Key([]byte(password), salt, hermesScryptN, hermesScryptR, hermesScryptP, hermesScryptLen)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

var _ assistantkey.Keeper = (*Executor)(nil)
