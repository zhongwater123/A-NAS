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

// progressEvery is how often a download saves how far it has come.
const progressEvery = 2 * time.Second

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
	// Archives is apt's cache of downloaded packages, whose files show how
	// far a download has come; "" shows none until it ends.
	Archives string
	Now      func() time.Time
	// FreeBytes returns the free space of the file system holding a path.
	FreeBytes func(path string) (int64, error)
	// ProgressEvery is how often a download saves its progress; 0 is
	// progressEvery.
	ProgressEvery time.Duration
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
	result, err := Sync(s.Staging, s.Local, func(need int64) error {
		if missing := s.missingSpace(need); missing > 0 {
			return &spaceError{missing: missing}
		}
		return nil
	})
	if err != nil {
		problem := &Problem{Code: ProblemStaging, Detail: err.Error()}
		var space *spaceError
		if errors.As(err, &space) {
			problem = &Problem{Code: ProblemDiskSpace, Detail: strconv.FormatInt(space.missing, 10)}
		}
		s.checkFailed(state, problem)
		return false, err
	}
	return result.Files > 0 || result.Removed > 0, nil
}

// spaceError refuses a push the system disk has no room for.
type spaceError struct{ missing int64 }

func (e *spaceError) Error() string {
	return fmt.Sprintf("the system disk lacks %d bytes for the push", e.missing)
}

// checkFailed leaves the problem a check met in the state and records it,
// once: a source that stays unreachable is one record, not one an hour. A
// rollback stays on the page; the failed check is in the records.
func (s *Service) checkFailed(state *State, problem *Problem) {
	if state.Phase != PhaseRolledBack {
		state.Phase, state.Problem = PhaseCheckFailed, problem
	}
	if len(state.Records) > 0 {
		if last := state.Records[0]; last.Kind == RecordCheckFailed && last.Problem != nil && last.Problem.Code == problem.Code {
			return
		}
	}
	state.Record(Record{At: s.now(), Kind: RecordCheckFailed, By: ByAutomatic, Problem: problem})
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
		s.checkFailed(state, &Problem{Code: ProblemNotInstalled, Detail: "a-nas is not installed with apt"})
		return nil
	}
	state.Current = current
	state.Suites = s.suites()
	output, err := s.Commands.Run(ctx, "apt-get", "update", "-o", "Dir::Etc::sourcelist="+s.Sources,
		"-o", "Dir::Etc::sourceparts=-", "-o", "APT::Get::List-Cleanup=0", "-o", "APT::Update::Error-Mode=any")
	if problem := updateProblem(output, err); problem != nil {
		s.checkFailed(state, problem)
		return nil
	}
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
		state.Candidate, state.DownloadBytes, state.DownloadedBytes = "", 0, 0
		// A rollback stays on the page, with its reason, until a newer
		// version replaces it; one that left the device failing its check,
		// until the check passes again, as after a repair by the runbook.
		if state.Phase != PhaseRolledBack ||
			(state.Problem != nil && state.Problem.Code == ProblemRollback && s.healthCheck(ctx) == nil) {
			state.Phase, state.Problem = PhaseUpToDate, nil
		}
		return nil
	}
	files, err := s.downloads(ctx, s.releaseArguments(ctx, candidate))
	if err != nil {
		s.checkFailed(state, &Problem{Code: ProblemInstall, Detail: err.Error()})
		return nil
	}
	state.Candidate, state.Problem = candidate, nil
	state.DownloadBytes, state.DownloadedBytes = downloadTotal(files), 0
	if state.DownloadBytes == 0 {
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
			for suite := range strings.FieldsSeq(value) {
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

// packageState is what dpkg records of a package: its status, such as
// installed, unpacked or half-installed, and its version; "" for a package
// dpkg does not know.
func (s *Service) packageState(ctx context.Context, name string) (status, version string) {
	output, err := s.Commands.Run(ctx, "dpkg-query", "--show", "--showformat=${db:Status-Status} ${Version}", name)
	if err != nil {
		return "", ""
	}
	status, version, _ = strings.Cut(strings.TrimSpace(string(output)), " ")
	return status, version
}

// present reports whether dpkg has a package in a status, whole or not.
func present(status string) bool {
	return status != "" && status != "not-installed" && status != "config-files"
}

// installedVersion is the version dpkg has installed, or "" for a package
// it does not know or has not installed whole.
func (s *Service) installedVersion(ctx context.Context, name string) string {
	if status, version := s.packageState(ctx, name); status == "installed" {
		return version
	}
	return ""
}

// landed reports whether the release is installed whole at version: a-nas
// and a-nas-host, and a-nas-console and a-nas-assistant-trial where dpkg
// has them in any status.
func (s *Service) landed(ctx context.Context, version string) bool {
	for i, name := range releasePackages {
		status, installed := s.packageState(ctx, name)
		if i >= 2 && !present(status) {
			continue
		}
		if status != "installed" || installed != version {
			return false
		}
	}
	return true
}

// untouched reports whether an install that stopped left nothing to go back
// from: the release is whole at version, and dpkg has nothing half done, not
// even a dependency it had begun to unpack.
func (s *Service) untouched(ctx context.Context, version string) bool {
	if !s.landed(ctx, version) {
		return false
	}
	output, err := s.Commands.Run(ctx, "dpkg", "--audit")
	return err == nil && strings.TrimSpace(string(output)) == ""
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
// a-nas-assistant-trial where dpkg has them. One that a failed install or a
// cut left unpacked or half installed counts, so that going back rewrites it.
func (s *Service) releaseArguments(ctx context.Context, version string) []string {
	var arguments []string
	for i, name := range releasePackages {
		if i >= 2 {
			if status, _ := s.packageState(ctx, name); !present(status) {
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

// download is a file installing a release fetches over the network: its
// name in apt's cache and its size.
type download struct {
	name string
	size int64
}

// downloads lists what installing the packages has to fetch over the
// network; a file: source has nothing to fetch.
func (s *Service) downloads(ctx context.Context, packages []string) ([]download, error) {
	output, err := s.Commands.Run(ctx, "apt-get", aptInstall(packages, "--print-uris", "-qq")...)
	if err != nil {
		return nil, fmt.Errorf("apt cannot install %s: %s", strings.Join(packages, " "), lastLines(string(output), 3))
	}
	var files []download
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !strings.HasPrefix(fields[0], "'") || strings.HasPrefix(fields[0], "'file:") {
			continue
		}
		if size, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			files = append(files, download{name: filepath.Base(fields[1]), size: size})
		}
	}
	return files, nil
}

func downloadTotal(files []download) int64 {
	var total int64
	for _, file := range files {
		total += file.size
	}
	return total
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
	files, err := s.downloads(ctx, packages)
	if err != nil {
		state.Problem = &Problem{Code: ProblemDownload, Detail: err.Error()}
		return nil
	}
	state.Phase, state.StartedAt = PhaseDownloading, s.now()
	state.DownloadBytes, state.DownloadedBytes = downloadTotal(files), 0
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	output, err := s.fetch(ctx, state, packages, files)
	if err != nil {
		state.Phase = PhaseAvailable
		state.Problem = &Problem{Code: ProblemDownload, Detail: lastLines(string(output), 3)}
		return nil
	}
	state.Phase, state.DownloadedBytes, state.Problem = PhaseReady, state.DownloadBytes, nil
	state.Record(Record{At: s.now(), Kind: RecordDownloaded, Version: state.Candidate, By: ByAutomatic})
	return nil
}

// fetch runs apt's download and, every few seconds, saves how far it has
// come for the page.
func (s *Service) fetch(ctx context.Context, state *State, packages []string, files []download) ([]byte, error) {
	var (
		output []byte
		err    error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		output, err = s.Commands.Run(ctx, "apt-get", aptInstall(packages, "--download-only")...)
	}()
	every := s.ProgressEvery
	if every <= 0 {
		every = progressEvery
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return output, err
		case <-ticker.C:
			if fetched := s.fetched(files); fetched != state.DownloadedBytes {
				state.DownloadedBytes = fetched
				// Only the page's progress: the download goes on without it.
				_ = s.Store.Save(*state)
			}
		}
	}
}

// fetched adds up what apt has of the files: each one it finished in its
// cache, and the part it has of the one it fetches into partial/.
func (s *Service) fetched(files []download) int64 {
	if s.Archives == "" {
		return 0
	}
	var total int64
	for _, file := range files {
		for _, path := range []string{filepath.Join(s.Archives, file.name), filepath.Join(s.Archives, "partial", file.name)} {
			if info, err := os.Stat(path); err == nil {
				total += min(info.Size(), file.size)
				break
			}
		}
	}
	return total
}

// missingSpace is how many bytes the system disk lacks for need and the
// margin; 0 when it has room or its free space cannot be read.
func (s *Service) missingSpace(need int64) int64 {
	if s.FreeBytes == nil {
		return 0
	}
	free, err := s.FreeBytes("/")
	if err != nil || free >= need+spaceMargin {
		return 0
	}
	return need + spaceMargin - free
}

// enoughSpace checks the system disk for need bytes and the margin, and
// leaves a problem in the state when it is short.
func (s *Service) enoughSpace(state *State, need int64) bool {
	if missing := s.missingSpace(need); missing > 0 {
		state.Problem = &Problem{Code: ProblemDiskSpace, Detail: strconv.FormatInt(missing, 10)}
		return false
	}
	return true
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
		if s.untouched(ctx, state.From) {
			s.notInstalled(state, problem)
			return nil
		}
		return s.goBack(ctx, state, problem)
	}
	return s.verify(ctx, state)
}

// notInstalled ends an install that stopped before it changed the release,
// as on apt's lock or a file the source lacks: the device is still whole on
// From, so nothing goes back, and the version is offered again.
func (s *Service) notInstalled(state *State, problem *Problem) {
	state.Record(Record{At: s.now(), Kind: RecordFailed, Version: state.Target, From: state.From, By: state.By, Problem: problem})
	state.Phase, state.Problem = PhaseReady, problem
	state.Target, state.From, state.By, state.BackupName = "", "", "", ""
}

// installVersion installs the release at version and reports what went wrong.
// --reinstall: a run cut short while dpkg unpacked a package leaves it half
// installed at the version dpkg still records, which apt takes for installed
// when the way back asks for that version.
func (s *Service) installVersion(ctx context.Context, version string) *Problem {
	output, err := s.Commands.Run(ctx, "apt-get", aptInstall(s.releaseArguments(ctx, version), "--reinstall")...)
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
// check fails too. A run that a power cut stops here resumes it, with the
// cause and the copy it saved.
func (s *Service) goBack(ctx context.Context, state *State, cause *Problem) error {
	if cause == nil {
		cause = &Problem{Code: ProblemInstall, Detail: "the install was interrupted"}
	}
	failed := state.Target
	state.Phase, state.Problem, state.Reverting = PhaseRollingBack, cause, false
	state.Reject(failed)
	// An install cut short before its check has not looked for the copy yet.
	if state.BackupName == "" {
		state.BackupName = s.backupOf(state.From, state.StartedAt)
	}
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	// A service of the failed version that kept failing has used up its
	// start limit, and the version before's postinst could not restart it.
	_, _ = s.Commands.Run(ctx, "systemctl", "reset-failed")
	kind, problem := RecordRolledBack, cause
	if failure := s.installVersion(ctx, state.From); failure != nil {
		kind, problem = RecordFailed, &Problem{Code: ProblemRollback, Detail: cause.Detail + "\n" + failure.Detail}
	} else if failure := s.healthCheck(ctx); failure != nil {
		if restored := s.restoreDatabases(ctx, state.BackupName); restored != nil || s.healthCheck(ctx) != nil {
			// Back on the version before, which fails its check too: the
			// record says so, as the page does.
			kind, problem = RecordFailed, &Problem{Code: ProblemRollback, Detail: cause.Detail + "\n" + failure.Detail}
		}
	}
	state.Record(Record{At: s.now(), Kind: kind, Version: failed, From: state.From, By: state.By, Problem: problem})
	state.Current, state.Phase, state.Problem = state.From, PhaseRolledBack, problem
	state.Candidate, state.DownloadBytes, state.DownloadedBytes = "", 0, 0
	state.Target, state.From, state.By, state.BackupName = "", "", "", ""
	return nil
}

// revert goes back to the version before the last install that passed, as
// the administrator asked; the version left is not offered again.
func (s *Service) revert(ctx context.Context, state *State, by string) error {
	if state.Phase.Busy() || state.Previous == "" {
		return nil
	}
	if !s.available(ctx, state.Previous) {
		state.Previous = ""
		return nil
	}
	state.Phase, state.Reverting, state.Target, state.From, state.By = PhaseRollingBack, true, state.Previous, state.Current, by
	state.StartedAt, state.Problem = s.now(), nil
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	return s.reverting(ctx, state)
}

// reverting installs the version an administrator went back to: after
// revert, or in a run that finds a revert a power cut stopped.
func (s *Service) reverting(ctx context.Context, state *State) error {
	left, by := state.From, state.By
	if problem := s.installVersion(ctx, state.Target); problem != nil {
		// Back onto the version it left, which passed its check, unless
		// nothing of it changed.
		if !s.untouched(ctx, left) {
			if again := s.installVersion(ctx, left); again != nil {
				problem = &Problem{Code: ProblemRollback, Detail: problem.Detail + "\n" + again.Detail}
			}
		}
		state.Record(Record{At: s.now(), Kind: RecordFailed, Version: state.Target, From: left, By: by, Problem: problem})
		state.Phase, state.Problem = PhaseUpToDate, problem
	} else {
		state.Problem = s.healthCheck(ctx)
		state.Reject(left)
		state.Record(Record{At: s.now(), Kind: RecordReverted, Version: state.Target, From: left, By: by, Problem: state.Problem})
		state.Current, state.Previous, state.InstalledAt, state.Phase = state.Target, "", s.now(), PhaseUpToDate
	}
	state.Reverting, state.Target, state.From, state.By = false, "", "", ""
	return nil
}

// boot finishes an install, rollback or revert that a power cut or reboot
// stopped: dpkg completes what it had unpacked; an install that landed whole
// is checked as after any install, one that changed nothing is offered
// again, and any other goes back.
func (s *Service) boot(ctx context.Context, state *State) error {
	if !state.Phase.Busy() || state.Phase == PhaseDownloading {
		if state.Phase == PhaseDownloading {
			state.Phase = PhaseAvailable
		}
		return nil
	}
	_, _ = s.Commands.Run(ctx, "dpkg", "--configure", "-a")
	switch {
	case state.Phase == PhaseInstalling && s.landed(ctx, state.Target):
		return s.verify(ctx, state)
	case state.Phase == PhaseInstalling && s.untouched(ctx, state.From):
		// Stopped before it changed the release.
		s.notInstalled(state, &Problem{Code: ProblemInstall, Detail: "the install was interrupted before it changed anything"})
		return nil
	case state.Phase == PhaseInstalling:
		return s.goBack(ctx, state, &Problem{Code: ProblemInstall, Detail: "the install was interrupted"})
	case state.Reverting:
		return s.reverting(ctx, state)
	}
	// Going back after a failed install was interrupted: finish it.
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
