package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeHost plays apt, dpkg and a-nas-check for the service.
type fakeHost struct {
	installed    map[string]string
	versions     []string // the versions of a-nas the source has, oldest first
	updateOutput string
	updateErr    error
	checkFails   map[string]bool // a-nas versions whose a-nas-check fails
	installFails map[string]bool // versions apt fails to install before it changes anything
	// brokenInstalls are versions whose install fails while dpkg unpacks
	// a-nas, which it leaves half installed at the version before.
	brokenInstalls map[string]bool
	// onInstall runs as apt starts to install a version of a-nas, and
	// onDownload as apt downloads.
	onInstall  func(version string)
	onDownload func()
	// halfInstalled are packages a cut left half installed, at the version
	// in installed; apt rewrites one only when told --reinstall.
	halfInstalled map[string]bool
	uris          string
	content       []string // installed content packages
	removed       []string
	calls         []string
}

func newFakeHost() *fakeHost {
	return &fakeHost{installed: map[string]string{"a-nas": "1", "a-nas-host": "1"}, versions: []string{"1", "2"},
		checkFails: map[string]bool{}, installFails: map[string]bool{}, brokenInstalls: map[string]bool{},
		halfInstalled: map[string]bool{}}
}

func (h *fakeHost) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	h.calls = append(h.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	switch name {
	case "dpkg-query":
		if strings.HasPrefix(args[1], "--showformat=${db:Status-Status} ${Package}") {
			var lines []string
			for _, name := range h.content {
				lines = append(lines, "installed "+name)
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
		if version, ok := h.installed[args[2]]; ok {
			if h.halfInstalled[args[2]] {
				return []byte("half-installed " + version), nil
			}
			return []byte("installed " + version), nil
		}
		return nil, errors.New("not installed")
	case "dpkg":
		if args[0] == "--audit" && len(h.halfInstalled) > 0 {
			return []byte("The following packages are only half installed, due to problems during\ninstallation."), nil
		}
		if args[0] == "--compare-versions" {
			if args[1] > args[3] {
				return nil, nil
			}
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	case "apt-get":
		switch {
		case args[0] == "update":
			return []byte(h.updateOutput), h.updateErr
		case args[0] == "remove" && args[1] == "-s":
			var lines []string
			for _, name := range args[2:] {
				lines = append(lines, "Remv "+name+" [1]")
			}
			return []byte(strings.Join(lines, "\n")), nil
		case args[0] == "remove":
			h.removed = append(h.removed, args[2:]...)
			return nil, nil
		case slices.Contains(args, "--print-uris"):
			return []byte(h.uris), nil
		case slices.Contains(args, "--download-only"):
			if h.onDownload != nil {
				h.onDownload()
			}
			return nil, nil
		}
		for _, argument := range args {
			if name, version, ok := strings.Cut(argument, "="); ok && !strings.Contains(argument, "::") {
				if name == "a-nas" && h.onInstall != nil {
					h.onInstall(version)
				}
				if h.installFails[version] {
					return []byte("E: Could not get lock /var/lib/dpkg/lock-frontend"), errors.New("exit status 100")
				}
				if name == "a-nas" && h.brokenInstalls[version] {
					h.halfInstalled[name] = true
					return []byte("E: Sub-process /usr/bin/dpkg returned an error code (1)"), errors.New("exit status 100")
				}
				if h.installed[name] == version && !slices.Contains(args, "--reinstall") {
					continue // apt: already the newest version
				}
				h.installed[name] = version
				delete(h.halfInstalled, name)
			}
		}
		return nil, nil
	case "apt-cache":
		switch args[0] {
		case "policy":
			return []byte(fmt.Sprintf("a-nas:\n  Installed: %s\n  Candidate: %s\n", h.installed["a-nas"], h.versions[len(h.versions)-1])), nil
		case "madison":
			var lines []string
			for _, version := range h.versions {
				lines = append(lines, " a-nas | "+version+" | file:/var/local/a-nas-repository dev/main amd64 Packages")
			}
			return []byte(strings.Join(lines, "\n")), nil
		case "depends":
			_, version, _ := strings.Cut(args[len(args)-1], "=")
			return []byte("a-nas\n  Depends: a-nas-host\n  Depends: a-nas-model-v" + version + "\n"), nil
		}
	case "a-nas-check":
		if h.checkFails[h.installed["a-nas"]] {
			return []byte("PASS a\nFAIL anas-api.service is failed; see journalctl -u anas-api.service\n"), errors.New("exit status 1")
		}
		return []byte("PASS everything\n"), nil
	case "systemctl", "sh":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s", name)
}

func (h *fakeHost) ran(prefix string) bool {
	return slices.ContainsFunc(h.calls, func(call string) bool { return strings.HasPrefix(call, prefix) })
}

func newService(t *testing.T, host *fakeHost) (*Service, *time.Time) {
	t.Helper()
	directory := t.TempDir()
	sources := filepath.Join(directory, "a-nas.sources")
	if err := os.WriteFile(sources, []byte("Types: deb\nURIs: file:/var/local/a-nas-repository\nSuites: release dev\nComponents: main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	return &Service{
		Store:    Store{Dir: filepath.Join(directory, "state")},
		Commands: host,
		Local:    filepath.Join(directory, "local"),
		Sources:  sources,
		Backups:  filepath.Join(directory, "backups"),
		Now:      func() time.Time { return now },
	}, &now
}

func load(t *testing.T, service *Service) State {
	t.Helper()
	state, err := service.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func run(t *testing.T, service *Service, kind string) State {
	t.Helper()
	if err := service.Run(context.Background(), Request{Kind: kind, By: "owner"}); err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	return load(t, service)
}

func TestACheckFindsTheNewerVersionAPushBrought(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	service.Staging = filepath.Join(t.TempDir(), "repository")
	writeFile(t, filepath.Join(service.Staging, "pool/main/a/a-nas/a-nas_2_amd64.deb"), "package")
	writeFile(t, filepath.Join(service.Staging, "dists/dev/InRelease"), "signed")
	state := run(t, service, RequestCheck)
	if state.Phase != PhaseReady || state.Candidate != "2" || state.Current != "1" {
		t.Fatalf("state = %+v, want ready to install 2 over 1", state)
	}
	if state.DownloadBytes != 0 {
		t.Fatalf("a file: source has nothing to download, got %d", state.DownloadBytes)
	}
	if !slices.Equal(state.Suites, []string{"release", "dev"}) {
		t.Fatalf("suites = %v", state.Suites)
	}
	for _, name := range []string{"pool/main/a/a-nas/a-nas_2_amd64.deb", "dists/dev/InRelease"} {
		if _, err := os.Stat(filepath.Join(service.Local, name)); err != nil {
			t.Fatalf("the push's %s is not in the local source: %v", name, err)
		}
	}
	if !host.ran("apt-get update -o Dir::Etc::sourcelist=" + service.Sources) {
		t.Fatalf("apt-get update did not read the A-NAS source alone: %v", host.calls)
	}
}

func TestANetworkSourceDownloadsWhenTheSettingsSaySo(t *testing.T) {
	host := newFakeHost()
	host.uris = "'https://example.invalid/a-nas_2_amd64.deb' a-nas_2_amd64.deb 45000000 SHA256:00\n'file:/x/b.deb' b.deb 7 SHA256:00\n"
	service, _ := newService(t, host)
	state := run(t, service, RequestTick)
	if state.Phase != PhaseReady || state.DownloadBytes != 45000000 {
		t.Fatalf("state = %+v, want downloaded", state)
	}
	if !host.ran("apt-get install -y --no-install-recommends --allow-downgrades -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=600 --download-only a-nas=2 a-nas-host=2") {
		t.Fatalf("no download: %v", host.calls)
	}
}

func TestATickWithoutAPushChecksOnceADay(t *testing.T) {
	host := newFakeHost()
	service, now := newService(t, host)
	run(t, service, RequestTick)
	host.calls = nil
	*now = now.Add(time.Hour)
	run(t, service, RequestTick)
	if host.ran("apt-get update") {
		t.Fatalf("checked again within the day: %v", host.calls)
	}
	*now = now.Add(24 * time.Hour)
	run(t, service, RequestTick)
	if !host.ran("apt-get update") {
		t.Fatal("did not check after a day")
	}
}

func TestASignatureAptRefusesFailsTheCheck(t *testing.T) {
	host := newFakeHost()
	host.updateOutput = "W: OpenPGP signature verification failed: file:/var/local/a-nas-repository dev InRelease: Sub-process /usr/bin/sqv returned an error code (1)\n"
	service, _ := newService(t, host)
	state := run(t, service, RequestCheck)
	if state.Phase != PhaseCheckFailed || state.Problem == nil || state.Problem.Code != ProblemSignature {
		t.Fatalf("state = %+v, want a signature problem", state)
	}
	if len(state.Records) != 1 || state.Records[0].Kind != RecordCheckFailed {
		t.Fatalf("records = %+v", state.Records)
	}
}

func TestAnInstallThatPassesItsCheckIsKept(t *testing.T) {
	host := newFakeHost()
	host.content = []string{"a-nas-model-v1", "a-nas-model-v2", "a-nas-model-v0"}
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	state := run(t, service, RequestInstall)
	if state.Phase != PhaseUpToDate || state.Current != "2" || state.Previous != "1" {
		t.Fatalf("state = %+v, want 2 installed with 1 before it", state)
	}
	if host.installed["a-nas-host"] != "2" {
		t.Fatal("a-nas-host was not installed at the same version")
	}
	if len(state.Records) != 1 || state.Records[0].Kind != RecordUpdated || state.Records[0].By != "owner" {
		t.Fatalf("records = %+v", state.Records)
	}
	// The content of 2 and of 1 stays for going back; older content goes.
	if !slices.Equal(host.removed, []string{"a-nas-model-v0"}) {
		t.Fatalf("removed %v, want only the content neither version needs", host.removed)
	}
}

func TestAFailedCheckGoesBackAndIsNotOfferedAgain(t *testing.T) {
	host := newFakeHost()
	host.checkFails["2"] = true
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	state := run(t, service, RequestInstall)
	if state.Phase != PhaseRolledBack || state.Current != "1" || host.installed["a-nas"] != "1" {
		t.Fatalf("state = %+v, installed %v, want back on 1", state, host.installed)
	}
	if state.Problem == nil || !strings.Contains(state.Problem.Detail, "FAIL anas-api.service") {
		t.Fatalf("problem = %+v, want the failed check", state.Problem)
	}
	if !slices.Contains(state.Rejected, "2") || state.Records[0].Kind != RecordRolledBack {
		t.Fatalf("state = %+v", state)
	}
	// The rollback and its reason stay on the page through later checks.
	state = run(t, service, RequestCheck)
	if state.Phase != PhaseRolledBack || state.Candidate != "" || state.Problem == nil || state.Problem.Code != ProblemCheck {
		t.Fatalf("a check after the rollback = %+v, want still rolled back, 2 not offered", state)
	}
	// A newer version replaces it.
	host.versions = []string{"1", "2", "3"}
	state = run(t, service, RequestCheck)
	if state.Phase != PhaseReady || state.Candidate != "3" || state.Problem != nil {
		t.Fatalf("a check that finds 3 = %+v, want 3 ready", state)
	}
}

func TestAnInstallAptFailsGoesBack(t *testing.T) {
	host := newFakeHost()
	host.brokenInstalls["2"] = true
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	state := run(t, service, RequestInstall)
	if state.Phase != PhaseRolledBack || state.Problem == nil || state.Problem.Code != ProblemInstall {
		t.Fatalf("state = %+v, want rolled back after the failed install", state)
	}
	if !host.ran("dpkg --configure -a") {
		t.Fatal("dpkg was not told to finish what it had unpacked")
	}
	if host.halfInstalled["a-nas"] || host.installed["a-nas"] != "1" || !slices.Contains(state.Rejected, "2") {
		t.Fatalf("a-nas %s, half installed %v, rejected %v; want 1 rewritten and 2 not offered again",
			host.installed["a-nas"], host.halfInstalled["a-nas"], state.Rejected)
	}
}

func TestAnInstallThatChangedNothingIsOfferedAgain(t *testing.T) {
	host := newFakeHost()
	// apt waited for its lock in vain: nothing was installed.
	host.installFails["2"] = true
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	state := run(t, service, RequestInstall)
	if state.Phase != PhaseReady || state.Candidate != "2" || state.Problem == nil || state.Problem.Code != ProblemInstall {
		t.Fatalf("state = %+v, want 2 ready again with the install's problem", state)
	}
	if slices.Contains(state.Rejected, "2") || state.Records[0].Kind != RecordFailed || state.Target != "" {
		t.Fatalf("state = %+v, want 2 not rejected and the install recorded as not done", state)
	}
	if host.ran("apt-get install -y --no-install-recommends --allow-downgrades -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=600 --reinstall a-nas=1") {
		t.Fatalf("went back although nothing changed: %v", host.calls)
	}
	// The next install tries it again.
	delete(host.installFails, "2")
	if state = run(t, service, RequestInstall); state.Current != "2" || state.Phase != PhaseUpToDate {
		t.Fatalf("the second install = %+v", state)
	}
}

func TestBootFinishesAnInterruptedInstall(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	state := load(t, service)
	// The power went while 2 was being installed, after dpkg unpacked it.
	state.Phase, state.Target, state.From, state.By = PhaseInstalling, "2", "1", "owner"
	if err := service.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	host.installed["a-nas"], host.installed["a-nas-host"] = "2", "2"
	state = run(t, service, RequestBoot)
	if !host.ran("dpkg --configure -a") || state.Phase != PhaseUpToDate || state.Current != "2" {
		t.Fatalf("state = %+v, calls %v", state, host.calls)
	}
}

func TestBootGoesBackWhenTheInterruptedInstallDidNotLand(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	state := State{Phase: PhaseInstalling, Current: "1", Target: "2", From: "1", Settings: DefaultSettings()}
	if err := service.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	// a-nas-host was installed at 2, a-nas not yet.
	host.installed["a-nas-host"] = "2"
	state = run(t, service, RequestBoot)
	if state.Phase != PhaseRolledBack || host.installed["a-nas"] != "1" || host.installed["a-nas-host"] != "1" {
		t.Fatalf("state = %+v, installed %v", state, host.installed)
	}
}

func TestBootOffersAnInstallThatChangedNothingAgain(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	// The power went before apt changed anything.
	state := State{Phase: PhaseInstalling, Current: "1", Candidate: "2", Target: "2", From: "1", By: "owner", Settings: DefaultSettings()}
	if err := service.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	state = run(t, service, RequestBoot)
	if state.Phase != PhaseReady || state.Candidate != "2" || slices.Contains(state.Rejected, "2") || state.Records[0].Kind != RecordFailed {
		t.Fatalf("state = %+v, want 2 ready again and not rejected", state)
	}
}

func TestAnInstallCutWhileADependencyUnpackedGoesBack(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	state := State{Phase: PhaseInstalling, Current: "1", Target: "2", From: "1", Settings: DefaultSettings()}
	if err := service.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	// The release is still on 1, but dpkg was unpacking a new model.
	host.installed["a-nas-model-v2"] = "1"
	host.halfInstalled["a-nas-model-v2"] = true
	state = run(t, service, RequestBoot)
	if state.Phase != PhaseRolledBack || !host.ran("apt-get install -y --no-install-recommends --allow-downgrades -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=600 --reinstall a-nas=1") {
		t.Fatalf("state = %+v, calls %v; want back on 1", state, host.calls)
	}
}

func TestBootJudgesTheWholeRelease(t *testing.T) {
	host := newFakeHost()
	host.installed["a-nas-console"] = "1"
	service, _ := newService(t, host)
	state := State{Phase: PhaseInstalling, Current: "1", Target: "2", From: "1", Settings: DefaultSettings()}
	if err := service.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	// a-nas and a-nas-host landed at 2; the cut came while dpkg unpacked
	// a-nas-console, which it still records at 1.
	host.installed["a-nas"], host.installed["a-nas-host"] = "2", "2"
	host.halfInstalled["a-nas-console"] = true
	state = run(t, service, RequestBoot)
	if state.Phase != PhaseRolledBack || state.Current != "1" {
		t.Fatalf("state = %+v, want back on 1", state)
	}
	// Going back rewrote the console too.
	if host.installed["a-nas-console"] != "1" || host.halfInstalled["a-nas-console"] {
		t.Fatalf("a-nas-console %s, half installed %v", host.installed["a-nas-console"], host.halfInstalled["a-nas-console"])
	}
}

func TestARollbackSavesItsReasonBeforeItInstalls(t *testing.T) {
	host := newFakeHost()
	host.checkFails["2"] = true
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	var saved State
	host.onInstall = func(version string) {
		if version == "1" {
			saved = load(t, service)
		}
	}
	run(t, service, RequestInstall)
	if saved.Phase != PhaseRollingBack || saved.Problem == nil || saved.Problem.Code != ProblemCheck || !slices.Contains(saved.Rejected, "2") {
		t.Fatalf("the state a cut during the rollback leaves = %+v, want its reason and 2 rejected", saved)
	}
}

func TestBootFinishesAnInterruptedRollback(t *testing.T) {
	host := newFakeHost()
	// 1 fails its check too, and there is no copy of the databases.
	host.checkFails["1"] = true
	host.installed["a-nas"], host.installed["a-nas-host"] = "2", "2"
	service, _ := newService(t, host)
	// Left without a reason, as runs before this fix saved it.
	state := State{Phase: PhaseRollingBack, Current: "1", Target: "2", From: "1", By: "owner", Settings: DefaultSettings()}
	if err := service.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	state = run(t, service, RequestBoot)
	if state.Phase != PhaseRolledBack || host.installed["a-nas"] != "1" || !slices.Contains(state.Rejected, "2") {
		t.Fatalf("state = %+v, installed %v", state, host.installed)
	}
	if record := state.Records[0]; record.Kind != RecordFailed || record.Problem == nil || record.Problem.Code != ProblemRollback {
		t.Fatalf("record = %+v, want the rollback that did not pass", record)
	}
}

func TestBootFinishesAnInterruptedRevert(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	run(t, service, RequestInstall)
	state := load(t, service)
	// The power went while going back from 2 to 1.
	state.Phase, state.Reverting, state.Target, state.From, state.By = PhaseRollingBack, true, "1", "2", "owner"
	if err := service.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	host.halfInstalled["a-nas"] = true
	state = run(t, service, RequestBoot)
	if state.Current != "1" || host.installed["a-nas"] != "1" || host.halfInstalled["a-nas"] || state.Reverting {
		t.Fatalf("state = %+v, installed %v; want the revert to 1 finished", state, host.installed)
	}
	if !slices.Contains(state.Rejected, "2") || slices.Contains(state.Rejected, "1") || state.Records[0].Kind != RecordReverted {
		t.Fatalf("state = %+v, want 2 left behind and 1 kept", state)
	}
}

func TestBootRewritesWhatACutLeftHalfInstalled(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	state := State{Phase: PhaseInstalling, Current: "1", Target: "2", From: "1", Settings: DefaultSettings()}
	if err := service.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	// dpkg was unpacking a-nas when the run was killed; it still records 1.
	host.halfInstalled["a-nas"] = true
	state = run(t, service, RequestBoot)
	if host.halfInstalled["a-nas"] || host.installed["a-nas"] != "1" {
		t.Fatalf("a-nas is %s, half installed %v; want 1 rewritten", host.installed["a-nas"], host.halfInstalled["a-nas"])
	}
	if state.Phase != PhaseRolledBack || state.Current != "1" || state.Records[0].Kind != RecordRolledBack {
		t.Fatalf("state = %+v", state)
	}
}

func TestARollbackWhoseCheckFailsTooIsRecordedAsFailed(t *testing.T) {
	host := newFakeHost()
	host.checkFails["1"], host.checkFails["2"] = true, true
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	state := run(t, service, RequestInstall)
	if state.Phase != PhaseRolledBack || state.Problem == nil || state.Problem.Code != ProblemRollback {
		t.Fatalf("state = %+v, want the rollback's problem", state)
	}
	if record := state.Records[0]; record.Kind != RecordFailed || record.Problem == nil || record.Problem.Code != ProblemRollback {
		t.Fatalf("record = %+v, want a failed rollback", record)
	}
	// The warning stays while 1 fails its check, and goes once it passes,
	// as after a repair by the runbook.
	if state = run(t, service, RequestCheck); state.Phase != PhaseRolledBack || state.Problem.Code != ProblemRollback {
		t.Fatalf("a check while 1 still fails = %+v", state)
	}
	host.checkFails["1"] = false
	if state = run(t, service, RequestCheck); state.Phase != PhaseUpToDate || state.Problem != nil {
		t.Fatalf("a check once 1 passes = %+v, want up to date", state)
	}
}

func TestRevertGoesToTheVersionBeforeAndLeavesTheOtherOut(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	run(t, service, RequestInstall)
	state := run(t, service, RequestRevert)
	if state.Current != "1" || host.installed["a-nas"] != "1" || state.Previous != "" {
		t.Fatalf("state = %+v", state)
	}
	if !slices.Contains(state.Rejected, "2") || state.Records[0].Kind != RecordReverted || state.Records[0].By != "owner" {
		t.Fatalf("state = %+v", state)
	}
}

func TestAPreviousVersionTheSourceDroppedIsNotOffered(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	if state := run(t, service, RequestInstall); state.Previous != "1" {
		t.Fatalf("previous = %q, want 1", state.Previous)
	}
	// A later push holds 2 and 3 alone.
	host.versions = []string{"2", "3"}
	if state := run(t, service, RequestCheck); state.Previous != "" || state.Candidate != "3" {
		t.Fatalf("state = %+v, want no previous and 3 offered", state)
	}
}

func TestASourceThatStaysUnreachableIsRecordedOnce(t *testing.T) {
	host := newFakeHost()
	host.updateOutput, host.updateErr = "E: Failed to fetch https://example.invalid/dists/dev/InRelease\n", errors.New("exit status 100")
	service, now := newService(t, host)
	for range 5 {
		run(t, service, RequestTick)
		*now = now.Add(time.Hour)
	}
	state := load(t, service)
	if state.Phase != PhaseCheckFailed || len(state.Records) != 1 {
		t.Fatalf("five failed hourly checks = %+v, want one record", state)
	}
	// Another problem is recorded.
	host.updateOutput = "W: OpenPGP signature verification failed\n"
	if state = run(t, service, RequestTick); len(state.Records) != 2 || state.Records[0].Problem.Code != ProblemSignature {
		t.Fatalf("records = %+v", state.Records)
	}
}

func TestADownloadShowsHowFarItHasCome(t *testing.T) {
	host := newFakeHost()
	host.uris = "'https://example.invalid/a.deb' a-nas_2_amd64.deb 100 SHA256:00\n'https://example.invalid/b.deb' a-nas-host_2_amd64.deb 50 SHA256:00\n"
	service, _ := newService(t, host)
	service.Archives = filepath.Join(t.TempDir(), "archives")
	service.ProgressEvery = 5 * time.Millisecond
	seen := int64(-1)
	host.onDownload = func() {
		// apt finished one file and has 40 bytes of the other.
		writeFile(t, filepath.Join(service.Archives, "a-nas-host_2_amd64.deb"), strings.Repeat("h", 50))
		writeFile(t, filepath.Join(service.Archives, "partial", "a-nas_2_amd64.deb"), strings.Repeat("a", 40))
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if state, err := service.Store.Load(); err == nil && state.DownloadedBytes == 90 {
				seen = state.DownloadedBytes
				return
			}
		}
	}
	state := run(t, service, RequestTick)
	if seen != 90 {
		t.Fatalf("the page saw %d bytes during the download, want 90", seen)
	}
	if state.Phase != PhaseReady || state.DownloadedBytes != 150 || state.DownloadBytes != 150 {
		t.Fatalf("state = %+v, want all 150 bytes downloaded", state)
	}
}

func TestAPushTheSystemDiskHasNoRoomForIsNotTaken(t *testing.T) {
	host := newFakeHost()
	service, _ := newService(t, host)
	service.Staging = filepath.Join(t.TempDir(), "repository")
	writeFile(t, filepath.Join(service.Staging, "pool/main/a/a-nas/a-nas_2_amd64.deb"), "package")
	writeFile(t, filepath.Join(service.Staging, "dists/dev/InRelease"), "signed")
	service.FreeBytes = func(string) (int64, error) { return 1 << 30, nil }
	state := run(t, service, RequestCheck)
	if state.Phase != PhaseCheckFailed || state.Problem == nil || state.Problem.Code != ProblemDiskSpace {
		t.Fatalf("state = %+v, want the push held for space", state)
	}
	if _, err := os.Stat(filepath.Join(service.Local, "pool/main/a/a-nas/a-nas_2_amd64.deb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the push was copied without room for it: %v", err)
	}
}

func TestAStaleRequestIsDroppedNotCarriedOut(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Now()
	if err := store.PutRequest(Request{Kind: RequestInstall, By: "owner"}); err != nil {
		t.Fatal(err)
	}
	if request, ok, err := store.TakeRequest(now.Add(-time.Minute)); err != nil || !ok || request.Kind != RequestInstall {
		t.Fatalf("a fresh request = %+v, %v, %v", request, ok, err)
	}
	if _, ok, err := store.TakeRequest(now.Add(-time.Minute)); err != nil || ok {
		t.Fatalf("a request taken twice: %v, %v", ok, err)
	}
	// Left an hour ago for a unit that did not start.
	if err := store.PutRequest(Request{Kind: RequestInstall, By: "owner"}); err != nil {
		t.Fatal(err)
	}
	hourAgo := now.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(store.Dir, "request.json"), hourAgo, hourAgo); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.TakeRequest(now.Add(-10 * time.Minute)); ok || !errors.Is(err, ErrStaleRequest) {
		t.Fatalf("a stale request = %v, %v; want dropped", ok, err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "request.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the stale request stayed")
	}
}

func TestDiskSpaceStopsADownload(t *testing.T) {
	host := newFakeHost()
	host.uris = "'https://example.invalid/a-nas_2_amd64.deb' a-nas_2_amd64.deb 45000000 SHA256:00\n"
	service, _ := newService(t, host)
	service.FreeBytes = func(string) (int64, error) { return 1 << 30, nil }
	state := run(t, service, RequestTick)
	if state.Phase != PhaseAvailable || state.Problem == nil || state.Problem.Code != ProblemDiskSpace {
		t.Fatalf("state = %+v, want the download held for space", state)
	}
}

func TestTheInstallWindowRunsPastMidnight(t *testing.T) {
	at := func(hour, minute int) time.Time { return time.Date(2026, 11, 13, hour, minute, 0, 0, time.UTC) }
	night := Settings{WindowStart: "23:00", WindowEnd: "02:00"}
	for _, test := range []struct {
		settings Settings
		at       time.Time
		want     bool
	}{
		{DefaultSettings(), at(3, 0), true},
		{DefaultSettings(), at(4, 59), true},
		{DefaultSettings(), at(5, 0), false},
		{DefaultSettings(), at(2, 59), false},
		{night, at(23, 30), true},
		{night, at(1, 0), true},
		{night, at(2, 0), false},
	} {
		if got := test.settings.InWindow(test.at); got != test.want {
			t.Errorf("%+v at %s: %v, want %v", test.settings, test.at.Format("15:04"), got, test.want)
		}
	}
	// The window that is open opened today, or yesterday past midnight.
	if got := DefaultSettings().WindowOpened(at(4, 30)); !got.Equal(at(3, 0)) {
		t.Errorf("the 03:00 window at 04:30 opened %s", got)
	}
	if got := night.WindowOpened(at(1, 0)); !got.Equal(at(23, 0).AddDate(0, 0, -1)) {
		t.Errorf("the 23:00 window at 01:00 opened %s", got)
	}
	if (Settings{WindowStart: "3:00", WindowEnd: "05:00"}).Validate() == nil {
		t.Fatal("a window time without two digits was accepted")
	}
}

func TestSyncTakesOnlyRegularFilesFromInsideTheStagingDirectory(t *testing.T) {
	directory := t.TempDir()
	staging, local := filepath.Join(directory, "staging"), filepath.Join(directory, "local")
	secret := filepath.Join(directory, "secret")
	writeFile(t, secret, "root's own")
	writeFile(t, filepath.Join(staging, "dists/dev/InRelease"), "signed")
	writeFile(t, filepath.Join(staging, "pool/main/a/a-nas/a-nas_2_amd64.deb"), "package")
	writeFile(t, filepath.Join(staging, "notes.txt"), "not part of a repository")
	if err := os.Symlink(secret, filepath.Join(staging, "pool/main/a/a-nas/stolen.deb")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(directory, filepath.Join(staging, "pool/outside")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(staging, "dists/dev/Packages"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Sync(staging, local, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 2 {
		t.Fatalf("copied %d files, want the two regular ones", result.Files)
	}
	for _, name := range []string{"pool/main/a/a-nas/stolen.deb", "pool/outside", "notes.txt", "dists/dev/Packages"} {
		if _, err := os.Lstat(filepath.Join(local, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s reached the local source", name)
		}
	}
	// Nothing changed: nothing is copied again.
	if result, err = Sync(staging, local, nil); err != nil || result.Files != 0 {
		t.Fatalf("second sync copied %d files, err %v", result.Files, err)
	}
}

func TestSyncRemovesWhatThePushNoLongerHas(t *testing.T) {
	directory := t.TempDir()
	staging, local := filepath.Join(directory, "staging"), filepath.Join(directory, "local")
	writeFile(t, filepath.Join(staging, "dists/dev/InRelease"), "signed 1")
	writeFile(t, filepath.Join(staging, "pool/main/a/a-nas/a-nas_1_amd64.deb"), "package 1")
	writeFile(t, filepath.Join(staging, "pool/main/a/a-nas-model-old/a-nas-model-old_1_all.deb"), "model")
	if _, err := Sync(staging, local, nil); err != nil {
		t.Fatal(err)
	}
	// The next push has 2 alone, and no old model.
	for _, name := range []string{"pool/main/a/a-nas/a-nas_1_amd64.deb", "pool/main/a/a-nas-model-old/a-nas-model-old_1_all.deb"} {
		if err := os.Remove(filepath.Join(staging, name)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(staging, "pool/main/a/a-nas/a-nas_2_amd64.deb"), "package 2")
	writeFile(t, filepath.Join(staging, "dists/dev/InRelease"), "signed, the second push")
	result, err := Sync(staging, local, nil)
	if err != nil || result.Files != 2 || result.Removed != 2 {
		t.Fatalf("Sync() = %+v, %v; want 2 copied and 2 removed", result, err)
	}
	for name, want := range map[string]bool{
		"pool/main/a/a-nas/a-nas_2_amd64.deb": true, "dists/dev/InRelease": true,
		"pool/main/a/a-nas/a-nas_1_amd64.deb": false, "pool/main/a/a-nas-model-old": false,
	} {
		if _, err := os.Stat(filepath.Join(local, name)); (err == nil) != want {
			t.Errorf("%s in the local source: %v, want %v", name, err == nil, want)
		}
	}
}

func TestSyncAsksForRoomBeforeItCopies(t *testing.T) {
	directory := t.TempDir()
	staging, local := filepath.Join(directory, "staging"), filepath.Join(directory, "local")
	writeFile(t, filepath.Join(staging, "dists/dev/InRelease"), "signed")
	writeFile(t, filepath.Join(staging, "pool/main/a/a-nas/a-nas_2_amd64.deb"), "package")
	full := errors.New("no room")
	var asked int64
	if _, err := Sync(staging, local, func(need int64) error { asked = need; return full }); !errors.Is(err, full) {
		t.Fatalf("Sync() = %v, want the refusal", err)
	}
	if asked != int64(len("signed")+len("package")) {
		t.Fatalf("asked for %d bytes", asked)
	}
	if _, err := os.Stat(local); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("copied without room")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
