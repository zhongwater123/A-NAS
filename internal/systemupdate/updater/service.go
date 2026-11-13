package updater

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Commands runs the host's commands. Run returns standard output and standard
// error together; a non-zero exit is an error.
type Commands interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// The packages of an A-NAS release, at one version; the last two only where
// they are installed.
var releasePackages = []string{"a-nas", "a-nas-host", "a-nas-console", "a-nas-assistant-trial"}

// contentPatterns are the content packages a release depends on
// (scripts/lib/content.sh): installed at version 1, named by their content.
var contentPatterns = []string{"a-nas-model-*", "a-nas-ai-runtime-*", "a-nas-search-runtime-*", "a-nas-qdrant-*",
	"a-nas-llama-*", "a-nas-assistant-runtime-*"}

// checkInterval is how often the timer checks without a push.
const checkInterval = 24 * time.Hour

// spaceMargin is what the system disk keeps free beyond what an update needs.
const spaceMargin int64 = 2 << 30

// Service carries out requests on a device installed with apt.
type Service struct {
	Store    Store
	Commands Commands
	// Staging is the staging account's directory a development machine
	// pushes the repository into (ANAS_UPDATE_STAGING); "" when not set.
	Staging string
	// Local is the directory the file: source reads, Sources the source's
	// file, Backups where the a-nas package's preinst copies the databases.
	Local   string
	Sources string
	Backups string
	Now     func() time.Time
	// FreeBytes returns the free space of the file system holding a path.
	FreeBytes func(path string) (int64, error)
}

// Run carries out a request and leaves the state for the Host Agent. Only
// one run is ever in progress (anas-update.service), so a state that says an
// install or rollback is under way was left by a run that a power cut,
// reboot or kill stopped: that is finished first.
func (s *Service) Run(ctx context.Context, request Request) error {
	state, err := s.Store.Load()
	if err != nil {
		return err
	}
	if request.By == "" {
		request.By = ByAutomatic
	}
	if state.Phase.Busy() && request.Kind != RequestBoot {
		err = s.boot(ctx, &state)
		if saveErr := s.Store.Save(state); err == nil {
			err = saveErr
		}
		if err != nil {
			return err
		}
	}
	switch request.Kind {
	case RequestTick:
		err = s.tick(ctx, &state)
	case RequestCheck:
		err = s.check(ctx, &state, true)
	case RequestDownload:
		err = s.download(ctx, &state)
	case RequestInstall:
		err = s.install(ctx, &state, request.By)
	case RequestRevert:
		err = s.revert(ctx, &state, request.By)
	case RequestBoot:
		err = s.boot(ctx, &state)
	default:
		return fmt.Errorf("no such update request: %q", request.Kind)
	}
	if saveErr := s.Store.Save(state); err == nil {
		err = saveErr
	}
	return err
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// tick takes a push, checks when the last check is a day old or the push
// brought something, and downloads when the settings say so.
func (s *Service) tick(ctx context.Context, state *State) error {
	if state.Phase.Busy() {
		return nil
	}
	pushed, err := s.syncStaging(state)
	if err != nil {
		return nil // recorded in the state; the next tick tries again
	}
	if pushed || s.now().Sub(state.CheckedAt) >= checkInterval || state.Phase == PhaseCheckFailed {
		if err := s.check(ctx, state, false); err != nil {
			return err
		}
	}
	if state.Phase == PhaseAvailable && state.Settings.AutoDownload {
		return s.download(ctx, state)
	}
	return nil
}

// syncStaging copies a push into the local source, and reports whether it
// brought a file. No staging directory, or none pushed yet, is no push.
func (s *Service) syncStaging(state *State) (bool, error) {
	if s.Staging == "" {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(s.Staging, "dists")); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	result, err := Sync(s.Staging, s.Local)
	if err != nil {
		state.Phase = PhaseCheckFailed
		state.Problem = &Problem{Code: ProblemStaging, Detail: err.Error()}
		return false, err
	}
	return result.Files > 0, nil
}

// check reads the source's signed index and finds the candidate.
func (s *Service) check(ctx context.Context, state *State, takePush bool) error {
	if state.Phase.Busy() {
		return nil
	}
	if takePush {
		if _, err := s.syncStaging(state); err != nil {
			state.CheckedAt = s.now()
			return nil
		}
	}
	state.CheckedAt = s.now()
	current := s.installedVersion(ctx, "a-nas")
	if current == "" {
		state.Phase = PhaseCheckFailed
		state.Problem = &Problem{Code: ProblemNotInstalled, Detail: "a-nas is not installed with apt"}
		return nil
	}
	state.Current = current
	state.Suites = s.suites()
	output, err := s.Commands.Run(ctx, "apt-get", "update", "-o", "Dir::Etc::sourcelist="+s.Sources,
		"-o", "Dir::Etc::sourceparts=-", "-o", "APT::Get::List-Cleanup=0", "-o", "APT::Update::Error-Mode=any")
	if problem := updateProblem(output, err); problem != nil {
		state.Phase = PhaseCheckFailed
		state.Problem = problem
		state.Record(Record{At: s.now(), Kind: RecordCheckFailed, By: ByAutomatic, Problem: problem})
		return nil
	}
	state.Problem = nil
	// The page offers to go back only while the source still has the version.
	if state.Previous != "" && !s.available(ctx, state.Previous) {
		state.Previous = ""
	}
	candidate, err := s.candidate(ctx)
	if err != nil {
		return err
	}
	newer := false
	if candidate != "" && !slices.Contains(state.Rejected, candidate) {
		newer = s.newer(ctx, candidate, current)
	}
	if !newer {
		state.Phase, state.Candidate, state.DownloadBytes, state.DownloadedBytes = PhaseUpToDate, "", 0, 0
		return nil
	}
	state.Candidate = candidate
	size, err := s.downloadSize(ctx, s.releaseArguments(ctx, candidate))
	if err != nil {
		state.Phase = PhaseCheckFailed
		state.Problem = &Problem{Code: ProblemInstall, Detail: err.Error()}
		return nil
	}
	state.DownloadBytes, state.DownloadedBytes = size, 0
	if size == 0 {
		state.Phase = PhaseReady
	} else {
		state.Phase = PhaseAvailable
	}
	return nil
}

// updateProblem reads what apt-get update said: a signature it refused, or a
// source it could not reach.
func updateProblem(output []byte, err error) *Problem {
	text := string(output)
	detail := lastLines(text, 3)
	for _, sign := range []string{"OpenPGP signature verification failed", "NO_PUBKEY", "is not signed",
		"The following signatures were invalid", "Signed-By"} {
		if strings.Contains(text, sign) {
			return &Problem{Code: ProblemSignature, Detail: detail}
		}
	}
	if err != nil || strings.Contains(text, "Failed to fetch") || strings.Contains(text, "\nE: ") ||
		strings.HasPrefix(text, "E: ") {
		return &Problem{Code: ProblemUnreachable, Detail: detail}
	}
	return nil
}

// lastLines returns the last n non-empty lines, for a problem's detail.
func lastLines(text string, n int) string {
	var lines []string
	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// suites lists the distributions the source file names.
func (s *Service) suites() []string {
	data, err := os.ReadFile(s.Sources)
	if err != nil {
		return nil
	}
	var suites []string
	for line := range strings.Lines(string(data)) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Suites:"); ok {
			for _, suite := range strings.Fields(value) {
				if suite == "./" {
					continue
				}
				if !slices.Contains(suites, suite) {
					suites = append(suites, suite)
				}
			}
		}
	}
	return suites
}

// installedVersion is the version dpkg has installed, or "" for a package
// it does not know or has not installed.
func (s *Service) installedVersion(ctx context.Context, name string) string {
	output, err := s.Commands.Run(ctx, "dpkg-query", "--show", "--showformat=${db:Status-Status} ${Version}", name)
	if err != nil {
		return ""
	}
	status, version, _ := strings.Cut(strings.TrimSpace(string(output)), " ")
	if status != "installed" {
		return ""
	}
	return version
}

// candidate is the version of a-nas apt would install.
func (s *Service) candidate(ctx context.Context) (string, error) {
	output, err := s.Commands.Run(ctx, "apt-cache", "policy", "a-nas")
	if err != nil {
		return "", fmt.Errorf("read the candidate: %w: %s", err, lastLines(string(output), 3))
	}
	for line := range strings.Lines(string(output)) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:"); ok {
			if value = strings.TrimSpace(value); value != "(none)" {
				return value, nil
			}
		}
	}
	return "", nil
}

// available reports whether the source still has a version of a-nas.
func (s *Service) available(ctx context.Context, version string) bool {
	output, err := s.Commands.Run(ctx, "apt-cache", "madison", "a-nas")
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(output)) {
		fields := strings.Split(line, "|")
		if len(fields) >= 2 && strings.TrimSpace(fields[1]) == version {
			return true
		}
	}
	return false
}

// newer reports whether a is a later Debian version than b.
func (s *Service) newer(ctx context.Context, a, b string) bool {
	_, err := s.Commands.Run(ctx, "dpkg", "--compare-versions", a, "gt", b)
	return err == nil
}

// releaseArguments are the release's packages at a version, as apt-get
// install takes them: a-nas and a-nas-host always, a-nas-console and
// a-nas-assistant-trial where they are installed.
func (s *Service) releaseArguments(ctx context.Context, version string) []string {
	var arguments []string
	for i, name := range releasePackages {
		if i >= 2 {
			if s.installedVersion(ctx, name) == "" {
				continue
			}
		}
		arguments = append(arguments, name+"="+version)
	}
	return arguments
}

// aptInstall is the deploy runbook's install command (step 6) for the
// release's packages. It waits for an apt the administrator runs by hand
// rather than failing on its lock.
func aptInstall(packages []string, extra ...string) []string {
	arguments := []string{"install", "-y", "--no-install-recommends", "--allow-downgrades",
		"-o", "Dpkg::Options::=--force-confold", "-o", "DPkg::Lock::Timeout=600"}
	arguments = append(arguments, extra...)
	return append(arguments, packages...)
}

// downloadSize is what installing the packages has to fetch over the
// network; a file: source has nothing to fetch.
func (s *Service) downloadSize(ctx context.Context, packages []string) (int64, error) {
	output, err := s.Commands.Run(ctx, "apt-get", aptInstall(packages, "--print-uris", "-qq")...)
	if err != nil {
		return 0, fmt.Errorf("apt cannot install %s: %s", strings.Join(packages, " "), lastLines(string(output), 3))
	}
	var total int64
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !strings.HasPrefix(fields[0], "'") || strings.HasPrefix(fields[0], "'file:") {
			continue
		}
		if size, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			total += size
		}
	}
	return total, nil
}

// download fetches the candidate's packages into apt's cache.
func (s *Service) download(ctx context.Context, state *State) error {
	if state.Phase != PhaseAvailable || state.Candidate == "" {
		return nil
	}
	if !s.enoughSpace(state, state.DownloadBytes) {
		return nil
	}
	packages := s.releaseArguments(ctx, state.Candidate)
	state.Phase = PhaseDownloading
	state.StartedAt = s.now()
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	output, err := s.Commands.Run(ctx, "apt-get", aptInstall(packages, "--download-only")...)
	if err != nil {
		state.Phase = PhaseAvailable
		state.Problem = &Problem{Code: ProblemDownload, Detail: lastLines(string(output), 3)}
		return nil
	}
	state.Phase, state.DownloadedBytes, state.Problem = PhaseReady, state.DownloadBytes, nil
	state.Record(Record{At: s.now(), Kind: RecordDownloaded, Version: state.Candidate, By: ByAutomatic})
	return nil
}

// enoughSpace checks the system disk for need bytes and the margin, and
// leaves a problem in the state when it is short.
func (s *Service) enoughSpace(state *State, need int64) bool {
	if s.FreeBytes == nil {
		return true
	}
	free, err := s.FreeBytes("/")
	if err != nil || free >= need+spaceMargin {
		return true
	}
	state.Problem = &Problem{Code: ProblemDiskSpace,
		Detail: strconv.FormatInt(need+spaceMargin-free, 10)}
	return false
}

// install installs the candidate, checks it, and goes back to the version
// before when the check fails.
func (s *Service) install(ctx context.Context, state *State, by string) error {
	if state.Phase != PhaseReady || state.Candidate == "" {
		return nil
	}
	if !s.enoughSpace(state, 0) {
		return nil
	}
	state.Phase, state.Target, state.From, state.By = PhaseInstalling, state.Candidate, state.Current, by
	state.StartedAt, state.Problem, state.BackupName = s.now(), nil, ""
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	if problem := s.installVersion(ctx, state.Target); problem != nil {
		return s.goBack(ctx, state, problem)
	}
	return s.verify(ctx, state)
}

// installVersion installs the release at version and reports what went wrong.
func (s *Service) installVersion(ctx context.Context, version string) *Problem {
	output, err := s.Commands.Run(ctx, "apt-get", aptInstall(s.releaseArguments(ctx, version))...)
	if err != nil {
		// A package left half done must not stop the next apt command.
		_, _ = s.Commands.Run(ctx, "dpkg", "--configure", "-a")
		return &Problem{Code: ProblemInstall, Detail: lastLines(string(output), 5)}
	}
	return nil
}

// verify runs a-nas-check on the version just installed: it passes, or the
// device goes back to the version before.
func (s *Service) verify(ctx context.Context, state *State) error {
	state.BackupName = s.backupOf(state.From, state.StartedAt)
	if problem := s.healthCheck(ctx); problem != nil {
		return s.goBack(ctx, state, problem)
	}
	s.keepContent(ctx, state.Target, state.From)
	state.Record(Record{At: s.now(), Kind: RecordUpdated, Version: state.Target, From: state.From, By: state.By})
	state.Previous, state.Current, state.InstalledAt = state.From, state.Target, s.now()
	state.Phase, state.Candidate, state.DownloadBytes, state.DownloadedBytes = PhaseUpToDate, "", 0, 0
	state.Target, state.From, state.By, state.BackupName = "", "", "", ""
	return nil
}

// healthCheck runs a-nas-check: nil when it passes, else the lines that failed.
func (s *Service) healthCheck(ctx context.Context) *Problem {
	output, err := s.Commands.Run(ctx, "a-nas-check")
	if err == nil {
		return nil
	}
	var failed []string
	for line := range strings.Lines(string(output)) {
		if strings.HasPrefix(line, "FAIL ") {
			failed = append(failed, strings.TrimSpace(line))
		}
	}
	if len(failed) == 0 {
		failed = []string{lastLines(string(output), 3)}
	}
	return &Problem{Code: ProblemCheck, Detail: strings.Join(failed, "\n")}
}

// goBack installs the version the install started from after a failed
// install or check, and restores the database copies when that version's
// check fails too.
func (s *Service) goBack(ctx context.Context, state *State, cause *Problem) error {
	failed := state.Target
	state.Phase = PhaseRollingBack
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	state.Reject(failed)
	state.Problem = cause
	// A service of the failed version that kept failing has used up its
	// start limit, and the version before's postinst could not restart it.
	_, _ = s.Commands.Run(ctx, "systemctl", "reset-failed")
	if problem := s.installVersion(ctx, state.From); problem != nil {
		state.Phase = PhaseRolledBack
		state.Problem = &Problem{Code: ProblemRollback, Detail: cause.Detail + "\n" + problem.Detail}
		state.Record(Record{At: s.now(), Kind: RecordFailed, Version: failed, From: state.From, By: state.By, Problem: state.Problem})
		return nil
	}
	if problem := s.healthCheck(ctx); problem != nil {
		if restored := s.restoreDatabases(ctx, state.BackupName); restored != nil || s.healthCheck(ctx) != nil {
			state.Problem = &Problem{Code: ProblemRollback, Detail: cause.Detail + "\n" + problem.Detail}
		}
	}
	state.Record(Record{At: s.now(), Kind: RecordRolledBack, Version: failed, From: state.From, By: state.By, Problem: cause})
	state.Current, state.Phase = state.From, PhaseRolledBack
	state.Candidate, state.DownloadBytes, state.DownloadedBytes = "", 0, 0
	state.Target, state.From, state.By = "", "", ""
	return nil
}

// revert goes back to the version before the last install that passed, as
// the administrator asked; the version left is not installed automatically
// again.
func (s *Service) revert(ctx context.Context, state *State, by string) error {
	if state.Phase.Busy() || state.Previous == "" {
		return nil
	}
	if !s.available(ctx, state.Previous) {
		state.Previous = ""
		return nil
	}
	state.Phase, state.Target, state.From, state.By = PhaseRollingBack, state.Previous, state.Current, by
	state.StartedAt, state.Problem = s.now(), nil
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	left := state.From
	if problem := s.installVersion(ctx, state.Target); problem != nil {
		// Back onto the version it left, which passed its check.
		state.Problem = problem
		if again := s.installVersion(ctx, left); again != nil {
			state.Problem = &Problem{Code: ProblemRollback, Detail: problem.Detail + "\n" + again.Detail}
		}
		state.Record(Record{At: s.now(), Kind: RecordFailed, Version: state.Target, From: left, By: by, Problem: state.Problem})
		state.Phase, state.Target, state.From, state.By = PhaseUpToDate, "", "", ""
		return nil
	}
	state.Problem = s.healthCheck(ctx)
	state.Reject(left)
	state.Record(Record{At: s.now(), Kind: RecordReverted, Version: state.Target, From: left, By: by, Problem: state.Problem})
	state.Current, state.Previous, state.InstalledAt = state.Target, "", s.now()
	state.Phase, state.Target, state.From, state.By = PhaseUpToDate, "", "", ""
	return nil
}

// boot finishes an install or rollback that a power cut or reboot stopped:
// dpkg completes what it had unpacked, then the check decides as after any
// install.
func (s *Service) boot(ctx context.Context, state *State) error {
	if !state.Phase.Busy() || state.Phase == PhaseDownloading {
		if state.Phase == PhaseDownloading {
			state.Phase = PhaseAvailable
		}
		return nil
	}
	_, _ = s.Commands.Run(ctx, "dpkg", "--configure", "-a")
	installed := s.installedVersion(ctx, "a-nas")
	if state.Phase == PhaseInstalling {
		if installed != state.Target {
			return s.goBack(ctx, state, &Problem{Code: ProblemInstall, Detail: "the install was interrupted"})
		}
		return s.verify(ctx, state)
	}
	// Going back was interrupted: finish it.
	return s.goBack(ctx, state, state.Problem)
}

// backupOf finds the database copy the a-nas package's preinst made for the
// install that started at started from version: the newest
// <time>-<version> directory made since.
func (s *Service) backupOf(version string, started time.Time) string {
	entries, err := os.ReadDir(s.Backups)
	if err != nil || version == "" {
		return ""
	}
	best := ""
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasSuffix(name, "-"+version) {
			continue
		}
		if info, err := entry.Info(); err == nil && !info.ModTime().Before(started.Add(-time.Minute)) && name > best {
			best = name
		}
	}
	return best
}

// restoreDatabases puts back the copies the a-nas package's preinst made
// before the install, as the deploy runbook's recovery does: the services
// that hold them stop, their journals go, the copies replace them.
func (s *Service) restoreDatabases(ctx context.Context, backup string) error {
	if backup == "" {
		return errors.New("no copy of the databases from before the install")
	}
	services := []string{"anas-api", "anas-photos", "anas-shares", "anas-family", "anas-host-agent"}
	if output, err := s.Commands.Run(ctx, "systemctl", append([]string{"stop"}, services...)...); err != nil {
		return fmt.Errorf("stop the services: %w: %s", err, lastLines(string(output), 3))
	}
	script := `set -e
name=$1
rm -f /var/lib/a-nas/*.db-wal /var/lib/a-nas/*.db-shm \
  /srv/a-nas/data/photos/catalog.db-wal /srv/a-nas/data/photos/catalog.db-shm \
  /srv/a-nas/data/shares/catalog.db-wal /srv/a-nas/data/shares/catalog.db-shm \
  /srv/a-nas/data/family/family.db-wal /srv/a-nas/data/family/family.db-shm
cp -a "/var/backups/a-nas/$name/system-disk/." /var/lib/a-nas/
for store in photos shares family; do
  if [ -d "/srv/a-nas/data/.a-nas-backups/$name/$store" ]; then
    cp -a "/srv/a-nas/data/.a-nas-backups/$name/$store/." "/srv/a-nas/data/$store/"
  fi
done`
	output, err := s.Commands.Run(ctx, "sh", "-c", script, "restore", backup)
	slices.Reverse(services)
	_, _ = s.Commands.Run(ctx, "systemctl", append([]string{"start"}, services...)...)
	if err != nil {
		return fmt.Errorf("restore the databases: %w: %s", err, lastLines(string(output), 3))
	}
	return nil
}

// keepContent removes the content packages neither the version installed
// nor the one before needs, so that going back needs no download and older
// models do not pile up.
func (s *Service) keepContent(ctx context.Context, current, previous string) {
	needed := map[string]bool{}
	for _, version := range []string{current, previous} {
		if version == "" {
			continue
		}
		for _, name := range []string{"a-nas", "a-nas-assistant-trial"} {
			output, err := s.Commands.Run(ctx, "apt-cache", "depends", "--no-recommends", "--no-suggests",
				"--no-conflicts", "--no-breaks", "--no-replaces", "--no-enhances", name+"="+version)
			if err != nil {
				if name == "a-nas" {
					return // unknown dependencies: remove nothing
				}
				continue
			}
			for line := range strings.Lines(string(output)) {
				if dependency, ok := strings.CutPrefix(strings.TrimSpace(line), "Depends: "); ok {
					needed[strings.Trim(dependency, "<>")] = true
				}
			}
		}
	}
	output, err := s.Commands.Run(ctx, "dpkg-query", append([]string{"--show", "--showformat=${db:Status-Status} ${Package}\n"},
		contentPatterns...)...)
	if err != nil && len(output) == 0 {
		return
	}
	var remove []string
	for line := range strings.Lines(string(output)) {
		status, name, _ := strings.Cut(strings.TrimSpace(line), " ")
		if status == "installed" && name != "" && !needed[name] {
			remove = append(remove, name)
		}
	}
	if len(remove) == 0 {
		return
	}
	// Only when apt would remove these alone.
	simulated, err := s.Commands.Run(ctx, "apt-get", append([]string{"remove", "-s"}, remove...)...)
	if err != nil {
		return
	}
	for line := range strings.Lines(string(simulated)) {
		if name, ok := strings.CutPrefix(line, "Remv "); ok {
			if name, _, _ = strings.Cut(name, " "); !slices.Contains(remove, name) {
				return
			}
		}
	}
	_, _ = s.Commands.Run(ctx, "apt-get", append([]string{"remove", "-y"}, remove...)...)
}
