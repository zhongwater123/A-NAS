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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	"golang.org/x/sys/unix"

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
)

// assistantTemplates are the assistant's unit templates (ADR 0024).
var assistantTemplates = []string{
	"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-api@.socket",
	"anas-assistant-cron@.service", "anas-assistant-curator@.service", "anas-assistant-curator@.timer",
	// The member's private network with its bridge to the model, which the
	// units above need: without them it would stop by itself, but only
	// after this returns, and it still runs as the member.
	"anas-assistant-net@.service",
}

// assistantEntries are the units that start a member's assistant: the API
// socket the File Broker connects to and the weekly curator timer. The others
// run only when these start them.
var assistantEntries = []string{"anas-assistant-api@.socket", "anas-assistant-curator@.timer"}

// assistantServices are what runs of a member's assistant, which deleting the key stops.
var assistantServices = []string{
	"anas-assistant@.service", "anas-assistant-api@.service", "anas-assistant-cron@.service",
	"anas-assistant-curator@.service", "anas-assistant-net@.service",
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

var (
	assistantSecretPattern = regexp.MustCompile(`^` + assistantSecret + `[0-9a-f]{64}\n?$`)
	hermesHashPattern      = regexp.MustCompile(`(?m)^    password_hash: "(scrypt\$[^"\n]+)"$`)
)

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

// assistantTokenPath is the member's instance secret: the password of Hermes's login, with
// which the File Broker signs in for the member's live session (decision 10).
func (e *Executor) assistantTokenPath(username string) string {
	return filepath.Join(e.assistantState(), "tokens", username+".env")
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
	incoming := path + ".incoming"
	_ = os.Remove(incoming)
	// The key reaches systemd-creds on its standard input, never on a command line. The
	// machine's own credential key (/var/lib/systemd/credential.secret, which the a-nas
	// package makes) encrypts it; a TPM would tie it to the firmware's state too, and a
	// firmware update would then lose it.
	if output, err := e.runner.Run(ctx, "systemd-creds", []string{"encrypt", "--name=" + assistantCredential, "--with-key=host", "-", incoming}, key); err != nil {
		_ = os.Remove(incoming)
		return assistantkey.State{}, commandError("encrypt the assistant's key", err, output)
	}
	if err := os.Chmod(incoming, 0o600); err != nil {
		_ = os.Remove(incoming)
		return assistantkey.State{}, fmt.Errorf("protect the assistant's key: %w", err)
	}
	if err := os.Rename(incoming, path); err != nil {
		_ = os.Remove(incoming)
		return assistantkey.State{}, fmt.Errorf("save the assistant's key: %w", err)
	}
	var failures []error
	for _, command := range [][]string{{"restart", "--no-block", assistantGatewayUnit}, {"start", "--no-block", assistantWakerUnit}} {
		if output, err := e.runner.Run(ctx, "systemctl", command, ""); err != nil {
			failures = append(failures, commandError(command[0]+" "+command[2], err, output))
		}
	}
	for _, username := range e.activeIdentities() {
		if err := e.prepareAssistant(ctx, username); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", username, err))
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
	if output, err := e.runner.Run(ctx, "systemctl", []string{"stop", assistantWakerUnit, assistantGatewayUnit}, ""); err != nil {
		failures = append(failures, commandError("stop the model gateway", err, output))
	}
	// A disabled member's units are masked and stopped already (disableAssistant).
	for _, username := range e.activeIdentities() {
		entries, err := e.assistantUnits(username, assistantEntries)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		services, err := e.assistantUnits(username, assistantServices)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if len(entries) > 0 {
			if output, err := e.runner.Run(ctx, "systemctl", append([]string{"disable", "--now", "--quiet"}, entries...), ""); err != nil {
				failures = append(failures, commandError("turn off "+username+"'s assistant", err, output))
			}
		}
		if len(services) > 0 {
			if output, err := e.runner.Run(ctx, "systemctl", append([]string{"stop"}, services...), ""); err != nil {
				failures = append(failures, commandError("stop "+username+"'s assistant", err, output))
			}
		}
	}
	if err := errors.Join(failures...); err != nil {
		return assistantkey.State{}, err
	}
	return e.AssistantKey(ctx)
}

// disableAssistant stops every way an assistant can run for a disabled member.
//
// Starts keep arriving while it runs: the Product Service keeps a signed-in
// member's instance (its socket starts the instance again), and the waker
// starts due tasks. In systemd a start queued after a stop job replaces it,
// and systemctl then fails with "Job for … canceled" while the unit runs on.
// So the units are masked before anything is stopped: from then on systemd
// refuses those starts, and the stop jobs complete. Disabling, which starts no
// jobs, comes first: systemctl leaves the boot and timer links of a unit that
// is already masked.
func (e *Executor) disableAssistant(ctx context.Context, username string) error {
	e.assistantMu.Lock()
	defer e.assistantMu.Unlock()
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
	return errors.Join(failures...)
}

// enableAssistant undoes disableAssistant for an active member: it unmasks the units and,
// while the administrator's key is saved, prepares the member's assistant and enables its
// entry points. Without the key the member gets them when it is saved.
func (e *Executor) enableAssistant(ctx context.Context, username string) error {
	e.assistantMu.Lock()
	defer e.assistantMu.Unlock()
	units, err := e.assistantUnits(username, assistantTemplates)
	if err != nil || len(units) == 0 {
		return err
	}
	if output, err := e.runner.Run(ctx, "systemctl", append([]string{"unmask"}, units...), ""); err != nil {
		return commandError("unmask assistant units", err, output)
	}
	return e.prepareAssistant(ctx, username)
}

// activeIdentities are the accounts the identity registry holds as enabled.
func (e *Executor) activeIdentities() []string {
	e.identitiesMu.Lock()
	defer e.identitiesMu.Unlock()
	var usernames []string
	for username, record := range e.identities {
		if record.Enabled {
			usernames = append(usernames, username)
		}
	}
	slices.Sort(usernames)
	return usernames
}

func (e *Executor) identityUID(username string) (int, bool) {
	e.identitiesMu.Lock()
	defer e.identitiesMu.Unlock()
	record, ok := e.identities[username]
	return record.UID, ok
}

// prepareAssistant makes the member's assistant ready to start and enables its entry points,
// while the key is saved and the data volume is mounted; the caller holds assistantMu. Each
// step is the same when it is done already.
func (e *Executor) prepareAssistant(ctx context.Context, username string) error {
	saved, err := e.assistantKeySaved()
	if err != nil || !saved {
		return err
	}
	entries, err := e.assistantUnits(username, assistantEntries)
	if err != nil || len(entries) == 0 {
		return err
	}
	uid, known := e.identityUID(username)
	if !known {
		return fmt.Errorf("no identity for %s", username)
	}
	if err := e.prepareAssistantSettings(username); err != nil {
		return err
	}
	// Without the volume the sandbox has nothing to bind and cannot start; the next identity
	// synchronization or saving of the key makes its folders.
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
	hash := ""
	if current, err := os.ReadFile(configPath); err == nil {
		if match := hermesHashPattern.FindSubmatch(current); match != nil && hermesHashMatches(string(match[1]), password) {
			hash = string(match[1])
		}
	}
	if hash == "" {
		if hash, err = hermesHash(password); err != nil {
			return err
		}
	}
	config := memberConfig(username, hash, managed)
	skillPath := filepath.Join(member, "skills", "anas-member", "SKILL.md")
	for path, contents := range map[string][]byte{
		configPath: config,
		skillPath:  bytes.ReplaceAll(skill, []byte("{{member}}"), []byte(username)),
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

// memberConfig is the member's config.yaml: the skill folders, the managed policy's MCP
// servers again (serve's discovery reads the raw user config) and Hermes's login.
func memberConfig(username, hash string, managed []byte) []byte {
	var config strings.Builder
	config.WriteString("# Written by the Host Agent (ADR 0024); read-only in the member's sandbox.\n")
	config.WriteString("skills:\n  external_dirs: [/usr/lib/a-nas/assistant/skills, /run/anas-assistant-skills]\n")
	inServers := false
	for line := range strings.Lines(string(managed)) {
		switch {
		case strings.HasPrefix(line, "mcp_servers:"):
			inServers = true
		case inServers && line != "\n" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#"):
			inServers = false
		}
		if inServers && line != "\n" {
			config.WriteString(line)
		}
	}
	fmt.Fprintf(&config, "dashboard:\n  basic_auth:\n    username: %s\n    password_hash: %q\n", username, hash)
	return []byte(config.String())
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
