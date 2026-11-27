package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeImage plays systemd-pull, systemd-sysext, systemd-sysupdate,
// systemctl, the product's maintainer scripts and the boot health check for
// the image service, on a device whose directories are temporary ones.
type fakeImage struct {
	service *ImageService
	calls   []string
	// badSignature makes systemd-pull refuse every SHA256SUMS.
	badSignature bool
	// failing are the product sequences whose boot health check fails.
	failing map[string]bool
	// versions are the products' Debian versions, by sequence.
	versions map[string]string
	merged   string
	bootID   string
	boots    int
	// counted: this boot's UKI had a boot counter, so boot-complete.target
	// is part of it; a boot of a UKI without one never reaches that target.
	counted bool
	// checking: the boot health check of a counted boot still runs.
	checking bool
}

func (h *fakeImage) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	h.calls = append(h.calls, strings.Join(append([]string{filepath.Base(name)}, args...), " "))
	s := h.service
	switch filepath.Base(name) {
	case "systemd-pull":
		if h.badSignature {
			return []byte("DOWNLOAD INVALID: Signature verification failed."), errors.New("exit status 1")
		}
		data, err := os.ReadFile(strings.TrimPrefix(args[len(args)-2], "file://"))
		if err != nil {
			return []byte("Failed to retrieve image file."), errors.New("exit status 1")
		}
		return nil, os.WriteFile(args[len(args)-1], data, 0o644)
	case "systemd-sysext":
		target, err := os.Readlink(filepath.Join(s.Extensions, "a-nas-product.raw"))
		if err != nil {
			return []byte("no product"), err
		}
		h.merge(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(target), "a-nas-product_"), ".raw"))
	case "health":
		if args[0] == "assess" && h.failing[h.merged] {
			return []byte("FAIL anas-api.service is failed; see journalctl -u anas-api.service\n" +
				"a-nas-health: this version fails: unit:anas-api.service\n"), errors.New("exit status 1")
		}
	case "systemd-sysupdate":
		uki := filepath.Join(s.ESP, "EFI", "Linux", "a-nas_"+args[1]+"+3-0.efi")
		return nil, os.WriteFile(uki, []byte("uki"), 0o644)
	case "systemctl":
		if args[0] == "is-active" && args[1] == "boot-complete.target" && (!h.counted || h.checking) {
			return []byte("inactive\n"), errors.New("exit status 3")
		}
		if args[0] == "is-active" {
			return []byte("active\n"), nil
		}
	}
	return nil, nil
}

func (h *fakeImage) ran(prefix string) bool {
	return slices.ContainsFunc(h.calls, func(call string) bool { return strings.HasPrefix(call, prefix) })
}

// merge makes product sequence the one the release file names, as
// systemd-sysext does when it merges it.
func (h *fakeImage) merge(sequence string) {
	h.merged = sequence
	release := fmt.Sprintf("ID=debian\nA_NAS_VERSION=%s\nA_NAS_SEQUENCE=%s\n", h.versions[sequence], sequence)
	if err := os.WriteFile(h.service.ProductRelease, []byte(release), 0o644); err != nil {
		panic(err)
	}
}

// boot starts the device on base, as systemd-boot, a-nas-product-select and
// systemd-sysext do: the product linked to that base is merged.
func (h *fakeImage) boot(base string) {
	s := h.service
	h.boots++
	h.bootID = fmt.Sprintf("boot-%d", h.boots)
	h.counted = s.ukiTries(base) != ukiBlessed
	if err := os.WriteFile(s.OSRelease, []byte("ID=debian\nIMAGE_ID=\"a-nas\"\nIMAGE_VERSION=\""+base+"\"\n"), 0o644); err != nil {
		panic(err)
	}
	if target, err := os.Readlink(filepath.Join(s.Products, base)); err == nil {
		if err := link(target, filepath.Join(s.Extensions, "a-nas-product.raw")); err != nil {
			panic(err)
		}
	}
	if _, err := h.Run(context.Background(), "systemd-sysext", "refresh"); err != nil {
		panic(err)
	}
}

// newImageService is a device that runs base and product 1001 with one kind
// of content, as its first disk left it, and a push directory.
func newImageService(t *testing.T) (*ImageService, *fakeImage) {
	t.Helper()
	directory := t.TempDir()
	host := &fakeImage{failing: map[string]bool{}, versions: map[string]string{"1001": "1.0.1"}}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	s := &ImageService{
		Store:          Store{Dir: filepath.Join(directory, "state")},
		Commands:       host,
		Staging:        filepath.Join(directory, "staging"),
		Backups:        filepath.Join(directory, "backups"),
		Now:            func() time.Time { return now },
		Updates:        filepath.Join(directory, "updates"),
		Sysext:         filepath.Join(directory, "sysext"),
		Extensions:     filepath.Join(directory, "extensions"),
		Products:       filepath.Join(directory, "products"),
		ESP:            filepath.Join(directory, "esp"),
		OSRelease:      filepath.Join(directory, "os-release"),
		ProductRelease: filepath.Join(directory, "extension-release.a-nas-product"),
		Health:         "/usr/lib/a-nas-image/health",
		Sysupdate:      "/usr/lib/systemd/systemd-sysupdate",
		Pull:           "/usr/lib/systemd/systemd-pull",
		Release:        "/usr/lib/a-nas/release",
		BootID:         func() string { return host.bootID },
		HTTP:           http.DefaultClient,
		BootWait:       time.Millisecond,
	}
	host.service = s
	for _, dir := range []string{s.Sysext, s.Extensions, s.Products, filepath.Join(s.ESP, "EFI", "Linux")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(s.Sysext, productFile("1001")), "product 1001")
	writeFile(t, filepath.Join(s.Extensions, contentFile("model", "1001")), "model 1001")
	writeFile(t, filepath.Join(s.ESP, "EFI", "Linux", "a-nas_1001.efi"), "uki")
	if err := s.productFor("1001", "1001"); err != nil {
		t.Fatal(err)
	}
	host.boot("1001")
	return s, host
}

// publish puts release sequence into dir as scripts/build-image.sh does:
// its product, its base and content where it brings new ones, its release
// file, and the product directory's SHA256SUMS.
func publish(t *testing.T, dir, sequence, version, base string, content map[string]string) {
	t.Helper()
	files := map[string]releaseFile{}
	put := func(name, data string) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if _, err := os.Stat(path); err != nil {
			writeFile(t, path, data)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(got)
		files[name] = releaseFile{Size: int64(len(got)), SHA256: hex.EncodeToString(sum[:])}
	}
	r := release{Sequence: sequence, Version: version, Architecture: systemdArchitecture(), HostID: "host-" + base,
		Base: base, Content: content}
	put(r.productPath(), "product "+sequence)
	for _, name := range r.basePaths() {
		put(name, name+" of base "+base)
	}
	for _, name := range r.contentPaths() {
		put(name, name)
	}
	r.Files = files
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "product", releaseFileName(sequence)), string(data))
	var sums strings.Builder
	entries, err := os.ReadDir(filepath.Join(dir, "product"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if name := entry.Name(); name != "SHA256SUMS" && name != "SHA256SUMS.gpg" {
			got, err := os.ReadFile(filepath.Join(dir, "product", name))
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(got), name)
		}
	}
	writeFile(t, filepath.Join(dir, "product", "SHA256SUMS"), sums.String())
	writeFile(t, filepath.Join(dir, "product", "SHA256SUMS.gpg"), "signature")
}

func runImage(t *testing.T, s *ImageService, kind, by string) State {
	t.Helper()
	if err := s.Run(context.Background(), Request{Kind: kind, By: by}); err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	state, err := s.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func linked(t *testing.T, name string) string {
	t.Helper()
	target, err := os.Readlink(name)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(target)
}

func TestImageCheckFindsTheReleaseAPushBrought(t *testing.T) {
	s, _ := newImageService(t)
	publish(t, s.Staging, "1002", "1.0.2", "1001", map[string]string{"model": "1002"})
	state := runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseReady || state.Candidate != "1.0.2" || state.Current != "1.0.1" || state.DownloadBytes != 0 {
		t.Fatalf("state = %+v", state)
	}
	if state.Image.Base != "1001" || state.Image.Product != "1001" || state.Image.Candidate != "1002" || state.Image.Restart {
		t.Fatalf("image = %+v", state.Image)
	}
	if _, err := os.Stat(filepath.Join(s.Updates, "product", productFile("1002"))); err != nil {
		t.Fatalf("the push was not taken: %v", err)
	}
}

func TestImageProductInstallsWithoutARestart(t *testing.T) {
	s, host := newImageService(t)
	host.versions["1002"] = "1.0.2"
	publish(t, s.Staging, "1002", "1.0.2", "1001", map[string]string{"model": "1002"})
	runImage(t, s, RequestCheck, "owner")
	state := runImage(t, s, RequestInstall, "owner")
	if state.Phase != PhaseUpToDate || state.Current != "1.0.2" || state.Previous != "1.0.1" {
		t.Fatalf("state = %+v", state)
	}
	if state.Records[0].Kind != RecordUpdated || state.Image.Product != "1002" || state.Image.PreviousProduct != "1001" {
		t.Fatalf("records = %+v, image = %+v", state.Records, state.Image)
	}
	if got := linked(t, filepath.Join(s.Extensions, "a-nas-product.raw")); got != productFile("1002") {
		t.Fatalf("the product link points at %s", got)
	}
	// The next boot of the base starts the same product.
	if got := linked(t, filepath.Join(s.Products, "1001")); got != productFile("1002") {
		t.Fatalf("base 1001 starts %s", got)
	}
	if _, err := os.Stat(filepath.Join(s.Extensions, contentFile("model", "1002"))); err != nil {
		t.Fatalf("the new content is not merged: %v", err)
	}
	for _, call := range []string{"preinst upgrade 1.0.1", "systemd-sysext refresh", "postinst configure 1.0.1", "health assess"} {
		if !host.ran(call) {
			t.Fatalf("%q did not run: %v", call, host.calls)
		}
	}
	if host.ran("systemd-sysupdate") || host.ran("systemctl reboot") {
		t.Fatalf("a product alone wrote a base or restarted: %v", host.calls)
	}
}

func TestImageProductThatBreaksACheckGoesBack(t *testing.T) {
	s, host := newImageService(t)
	host.versions["1002"] = "1.0.2"
	host.failing["1002"] = true
	publish(t, s.Staging, "1002", "1.0.2", "1001", nil)
	runImage(t, s, RequestCheck, "owner")
	state := runImage(t, s, RequestInstall, "owner")
	if state.Phase != PhaseRolledBack || state.Current != "1.0.1" || !slices.Contains(state.Rejected, "1.0.2") {
		t.Fatalf("state = %+v", state)
	}
	if state.Records[0].Kind != RecordRolledBack || state.Records[0].Problem.Code != ProblemCheck {
		t.Fatalf("records = %+v", state.Records)
	}
	if got := linked(t, filepath.Join(s.Extensions, "a-nas-product.raw")); got != productFile("1001") {
		t.Fatalf("the product link points at %s", got)
	}
	if got := linked(t, filepath.Join(s.Products, "1001")); got != productFile("1001") {
		t.Fatalf("base 1001 starts %s", got)
	}
	state = runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseRolledBack || state.Candidate != "" {
		t.Fatalf("the version gone back from is offered again: %+v", state)
	}
}

func TestImageCheckOffersANewerReleaseOnlyAfterAFailedRollbackIsRepaired(t *testing.T) {
	s, host := newImageService(t)
	host.versions["1002"] = "1.0.2"
	host.failing["1002"], host.failing["1001"] = true, true
	publish(t, s.Staging, "1002", "1.0.2", "1001", nil)
	runImage(t, s, RequestCheck, "owner")
	state := runImage(t, s, RequestInstall, "owner")
	if state.Phase != PhaseRolledBack || state.Problem == nil || state.Problem.Code != ProblemRollback {
		t.Fatalf("install = %+v, want a failed rollback", state)
	}
	publish(t, s.Staging, "1003", "1.0.3", "1001", nil)
	state = runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseRolledBack || state.Problem == nil || state.Problem.Code != ProblemRollback ||
		state.Candidate != "" || state.Image.Candidate != "" {
		t.Fatalf("a check while 1001 still fails = %+v, image = %+v", state, state.Image)
	}
	host.failing["1001"] = false
	state = runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseReady || state.Problem != nil || state.Candidate != "1.0.3" || state.Image.Candidate != "1003" {
		t.Fatalf("a check after repair = %+v, image = %+v, want 1003 ready", state, state.Image)
	}
}

// installBase checks and installs release 1003, which brings base 1003.
func installBase(t *testing.T, s *ImageService, host *fakeImage, by string) State {
	t.Helper()
	host.versions["1003"] = "1.0.3"
	publish(t, s.Staging, "1003", "1.0.3", "1003", map[string]string{"model": "1001"})
	state := runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseReady || !state.Image.Restart {
		t.Fatalf("check: %+v %+v", state, state.Image)
	}
	return runImage(t, s, RequestInstall, by)
}

func TestImageNewBaseWaitsForARestartAndIsKeptOnceItBoots(t *testing.T) {
	s, host := newImageService(t)
	state := installBase(t, s, host, "owner")
	if state.Phase != PhaseRestartPending || state.Target != "1.0.3" || state.Current != "1.0.1" {
		t.Fatalf("state = %+v", state)
	}
	if !host.ran("systemd-sysupdate update 1003") || !host.ran("health record") || host.ran("systemctl reboot") {
		t.Fatalf("calls = %v", host.calls)
	}
	// Each base starts its own product; what runs now is unchanged.
	if got := linked(t, filepath.Join(s.Products, "1003")); got != productFile("1003") {
		t.Fatalf("base 1003 starts %s", got)
	}
	if got := linked(t, filepath.Join(s.Extensions, "a-nas-product.raw")); got != productFile("1001") {
		t.Fatalf("the running product changed: %s", got)
	}
	runImage(t, s, RequestRestart, "owner")
	if !host.ran("systemctl reboot") {
		t.Fatalf("no restart: %v", host.calls)
	}

	host.boot("1003")
	state = runImage(t, s, RequestTick, "")
	if state.Phase != PhaseUpToDate || state.Current != "1.0.3" || state.Previous != "1.0.1" {
		t.Fatalf("state = %+v", state)
	}
	if state.Image.Base != "1003" || state.Image.PreviousBase != "1001" || state.Image.PreviousProduct != "1001" {
		t.Fatalf("image = %+v", state.Image)
	}
	if state.Records[0].Kind != RecordUpdated || state.Records[0].Version != "1.0.3" {
		t.Fatalf("records = %+v", state.Records)
	}
}

func TestImageNewBaseIsKeptOnlyOnceItsBootCompletes(t *testing.T) {
	s, host := newImageService(t)
	installBase(t, s, host, "owner")
	runImage(t, s, RequestRestart, "owner")
	host.boot("1003")
	host.checking = true
	state := runImage(t, s, RequestTick, "")
	if state.Phase != PhaseRestartPending || state.Current != "1.0.1" || len(state.Records) != 0 {
		t.Fatalf("settled before the boot check passed: %+v", state)
	}
	host.checking = false
	state = runImage(t, s, RequestTick, "")
	if state.Phase != PhaseUpToDate || state.Current != "1.0.3" {
		t.Fatalf("state = %+v", state)
	}
}

func TestImageBaseThatFailsItsBootCheckIsLeft(t *testing.T) {
	s, host := newImageService(t)
	installBase(t, s, host, "owner")
	runImage(t, s, RequestRestart, "owner")
	// Three tries failed: systemd-boot marked the base and went back.
	uki := filepath.Join(s.ESP, "EFI", "Linux")
	if err := os.Rename(filepath.Join(uki, "a-nas_1003+3-0.efi"), filepath.Join(uki, "a-nas_1003+0-3.efi")); err != nil {
		t.Fatal(err)
	}
	host.boot("1001")
	state := runImage(t, s, RequestTick, "")
	if state.Phase != PhaseRolledBack || state.Current != "1.0.1" || !slices.Contains(state.Rejected, "1.0.3") {
		t.Fatalf("state = %+v", state)
	}
	if state.Problem == nil || state.Problem.Code != ProblemBoot || state.Records[0].Kind != RecordRolledBack {
		t.Fatalf("problem = %+v, records = %+v", state.Problem, state.Records)
	}
	if state.Image.Base != "1001" || state.Image.Product != "1001" {
		t.Fatalf("image = %+v", state.Image)
	}
}

func TestImageRestartStillWaitsAfterABootIntoTheBaseBefore(t *testing.T) {
	s, host := newImageService(t)
	installBase(t, s, host, "owner")
	// Started by hand into the base before: the new one keeps its tries.
	host.boot("1001")
	state := runImage(t, s, RequestTick, "")
	if state.Phase != PhaseRestartPending || state.Image.BootID != host.bootID || len(state.Rejected) != 0 {
		t.Fatalf("state = %+v %+v", state, state.Image)
	}
}

func TestImageAutomaticInstallRestartsAtOnce(t *testing.T) {
	s, host := newImageService(t)
	state := installBase(t, s, host, ByAutomatic)
	if state.Phase != PhaseRestartPending || !host.ran("systemctl reboot") {
		t.Fatalf("state = %+v, calls = %v", state, host.calls)
	}
}

func TestImageRevertGoesBackToTheProductBefore(t *testing.T) {
	s, host := newImageService(t)
	host.versions["1002"] = "1.0.2"
	publish(t, s.Staging, "1002", "1.0.2", "1001", nil)
	runImage(t, s, RequestCheck, "owner")
	runImage(t, s, RequestInstall, "owner")
	state := runImage(t, s, RequestRevert, "owner")
	if state.Phase != PhaseUpToDate || state.Current != "1.0.1" || state.Previous != "" || !slices.Contains(state.Rejected, "1.0.2") {
		t.Fatalf("state = %+v", state)
	}
	if state.Records[0].Kind != RecordReverted {
		t.Fatalf("records = %+v", state.Records)
	}
	if got := linked(t, filepath.Join(s.Extensions, "a-nas-product.raw")); got != productFile("1001") {
		t.Fatalf("the product link points at %s", got)
	}
}

func TestImageRevertToTheBaseBeforeRestartsIntoIt(t *testing.T) {
	s, host := newImageService(t)
	installBase(t, s, host, "owner")
	runImage(t, s, RequestRestart, "owner")
	host.boot("1003")
	runImage(t, s, RequestTick, "")
	state := runImage(t, s, RequestRevert, "owner")
	if state.Phase != PhaseRestartPending || !state.Reverting || state.Target != "1.0.1" {
		t.Fatalf("state = %+v", state)
	}
	if _, err := os.Stat(filepath.Join(s.ESP, "EFI", "Linux", "a-nas_1003+0-0.efi")); err != nil {
		t.Fatalf("the base left is not marked: %v", err)
	}
	host.boot("1001")
	state = runImage(t, s, RequestTick, "")
	if state.Phase != PhaseUpToDate || state.Current != "1.0.1" || !slices.Contains(state.Rejected, "1.0.3") {
		t.Fatalf("state = %+v", state)
	}
	if state.Records[0].Kind != RecordReverted || state.Image.Base != "1001" || state.Image.Product != "1001" {
		t.Fatalf("records = %+v, image = %+v", state.Records, state.Image)
	}
}

func TestImageSignatureTheKeyRefusesFailsTheCheck(t *testing.T) {
	s, host := newImageService(t)
	publish(t, s.Staging, "1002", "1.0.2", "1001", nil)
	host.badSignature = true
	state := runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseCheckFailed || state.Problem == nil || state.Problem.Code != ProblemSignature {
		t.Fatalf("state = %+v", state)
	}
}

func TestImageReleaseFileThatDoesNotMatchItsSumIsRefused(t *testing.T) {
	s, _ := newImageService(t)
	publish(t, s.Staging, "1002", "1.0.2", "1001", nil)
	writeFile(t, filepath.Join(s.Staging, "product", releaseFileName("1002")), `{"sequence":"1002","version":"9.9.9"}`)
	state := runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseCheckFailed || state.Problem.Code != ProblemSignature {
		t.Fatalf("state = %+v", state)
	}
}

func TestImagePushThatLacksAFileIsNotOffered(t *testing.T) {
	s, _ := newImageService(t)
	publish(t, s.Staging, "1003", "1.0.3", "1003", nil)
	if err := os.Remove(filepath.Join(s.Staging, "os", "a-nas_1003.efi")); err != nil {
		t.Fatal(err)
	}
	state := runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseCheckFailed || state.Problem.Code != ProblemStaging ||
		!strings.Contains(state.Problem.Detail, "os/a-nas_1003.efi") {
		t.Fatalf("state = %+v", state)
	}
}

func TestImageDownloadsFromANetworkSource(t *testing.T) {
	s, host := newImageService(t)
	published := t.TempDir()
	host.versions["1003"] = "1.0.3"
	publish(t, published, "1003", "1.0.3", "1003", map[string]string{"model": "1003"})
	writeFile(t, filepath.Join(published, "os", "SHA256SUMS"), "sums")
	writeFile(t, filepath.Join(published, "os", "SHA256SUMS.gpg"), "signature")
	server := httptest.NewServer(http.FileServer(http.Dir(published)))
	defer server.Close()
	s.Source, s.Staging = func() (string, error) { return server.URL, nil }, ""

	state := runImage(t, s, RequestCheck, "owner")
	if state.Phase != PhaseAvailable || state.DownloadBytes == 0 || state.Candidate != "1.0.3" {
		t.Fatalf("check: %+v", state)
	}
	// A cut download resumes from what it has.
	partial := filepath.Join(s.Updates, "product", productFile("1003")+".partial")
	writeFile(t, partial, "prod")
	state = runImage(t, s, RequestDownload, "owner")
	if state.Phase != PhaseReady || state.DownloadedBytes != state.DownloadBytes || state.Records[0].Kind != RecordDownloaded {
		t.Fatalf("download: %+v", state)
	}
	for _, name := range []string{"os/SHA256SUMS", "os/a-nas_1003.usr.raw.xz", "product/" + productFile("1003"),
		"content-model/" + contentFile("model", "1003")} {
		if _, err := os.Stat(filepath.Join(s.Updates, filepath.FromSlash(name))); err != nil {
			t.Fatalf("%s was not downloaded: %v", name, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(s.Updates, "product", productFile("1003"))); string(got) != "product 1003" {
		t.Fatalf("the resumed download holds %q", got)
	}
	state = runImage(t, s, RequestInstall, "owner")
	if state.Phase != PhaseRestartPending {
		t.Fatalf("install: %+v", state)
	}
}

func TestImageDownloadThatDoesNotMatchItsSumIsDiscarded(t *testing.T) {
	s, _ := newImageService(t)
	published := t.TempDir()
	publish(t, published, "1002", "1.0.2", "1001", nil)
	server := httptest.NewServer(http.FileServer(http.Dir(published)))
	defer server.Close()
	s.Source, s.Staging = func() (string, error) { return server.URL, nil }, ""
	runImage(t, s, RequestCheck, "owner")
	// The same size, other bytes.
	writeFile(t, filepath.Join(published, "product", productFile("1002")), "produkt 1002")
	state := runImage(t, s, RequestDownload, "owner")
	if state.Phase != PhaseAvailable || state.Problem == nil || state.Problem.Code != ProblemDownload {
		t.Fatalf("state = %+v", state)
	}
	if _, err := os.Stat(filepath.Join(s.Updates, "product", productFile("1002"))); err == nil {
		t.Fatal("a file that does not match its sum was kept")
	}
}

func TestImageVacuumKeepsWhatTheVersionsRunAndBeforeUse(t *testing.T) {
	s, host := newImageService(t)
	for _, sequence := range []string{"1002", "1004"} {
		host.versions[sequence] = "1.0." + sequence[3:]
		publish(t, s.Staging, sequence, host.versions[sequence], "1001", map[string]string{"model": sequence})
		runImage(t, s, RequestCheck, "owner")
		state := runImage(t, s, RequestInstall, "owner")
		if state.Phase != PhaseUpToDate {
			t.Fatalf("%s: %+v", sequence, state)
		}
	}
	products, _ := filepath.Glob(filepath.Join(s.Sysext, "*.raw"))
	content := s.mergedContent()
	slices.Sort(content)
	if len(products) != 2 || !slices.Equal(content, []string{contentFile("model", "1002"), contentFile("model", "1004")}) {
		t.Fatalf("products = %v, content = %v", products, content)
	}
}

func TestUpdateSourceAppendsTheArchitecture(t *testing.T) {
	got, err := updateSource("https://updates.example.invalid/a-nas/")
	if err != nil || got != "https://updates.example.invalid/a-nas/"+systemdArchitecture() {
		t.Fatalf("updateSource = %q, %v", got, err)
	}
	for _, bad := range []string{"ftp://example.invalid/", "/var/lib/a-nas-updates", "https://example.invalid/?x=1"} {
		if _, err := updateSource(bad); err == nil {
			t.Fatalf("%q was taken", bad)
		}
	}
}
