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
	installFails map[string]bool // versions apt fails to install
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
		checkFails: map[string]bool{}, installFails: map[string]bool{}, halfInstalled: map[string]bool{}}
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
			return nil, nil
		}
		for _, argument := range args {
			if name, version, ok := strings.Cut(argument, "="); ok && !strings.Contains(argument, "::") {
				if h.installFails[version] {
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
	state = run(t, service, RequestCheck)
	if state.Phase != PhaseUpToDate || state.Candidate != "" {
		t.Fatalf("the rolled-back version is offered again: %+v", state)
	}
}

func TestAnInstallAptFailsGoesBack(t *testing.T) {
	host := newFakeHost()
	host.installFails["2"] = true
	service, _ := newService(t, host)
	run(t, service, RequestCheck)
	state := run(t, service, RequestInstall)
	if state.Phase != PhaseRolledBack || state.Problem == nil || state.Problem.Code != ProblemInstall {
		t.Fatalf("state = %+v, want rolled back after the failed install", state)
	}
	if !host.ran("dpkg --configure -a") {
		t.Fatal("dpkg was not told to finish what it had unpacked")
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
	host.installed["a-nas"] = "2"
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
	state = run(t, service, RequestBoot)
	if state.Phase != PhaseRolledBack || host.installed["a-nas"] != "1" {
		t.Fatalf("state = %+v", state)
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
	result, err := Sync(staging, local)
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
	if result, err = Sync(staging, local); err != nil || result.Files != 0 {
		t.Fatalf("second sync copied %d files, err %v", result.Files, err)
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
