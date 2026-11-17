package updater

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Where a device that runs the A-NAS image keeps what the service works with
// (deploy/image, scripts/build-image.sh).
const (
	imageUpdates        = "/var/lib/a-nas-updates"
	imageSysext         = "/var/lib/a-nas-sysext"
	imageExtensions     = "/var/lib/extensions"
	imageProducts       = "/var/lib/a-nas-image/product"
	imageESP            = "/efi"
	imageOSRelease      = "/usr/lib/os-release"
	imageProductRelease = "/usr/lib/extension-release.d/extension-release.a-nas-product"
	imageHealth         = "/usr/lib/a-nas-image/health"
	imageSysupdate      = "/usr/lib/systemd/systemd-sysupdate"
	imagePull           = "/usr/lib/systemd/systemd-pull"
	releaseScripts      = "/usr/lib/a-nas/release"
	bootIDFile          = "/proc/sys/kernel/random/boot_id"
)

// bootWait is how long a run after a restart waits for systemd to reach
// boot-complete.target, which the boot health check holds back.
const bootWait = 15 * time.Minute

// ImageService carries out requests on a device that runs the A-NAS image
// (ADR 0034 decision 2, docs/specs/system-update.md). A release there is a
// base image, a product and the content the product uses, each published by
// scripts/build-image.sh with a signed SHA256SUMS per directory, and
// product/a-nas-release_SEQUENCE.json names which ones a release is:
//
//   - The service checks a release file against the product directory's
//     signed SHA256SUMS (systemd-pull, the tool systemd-sysupdate uses), and
//     every other file against the SHA-256 the release file gives.
//   - A release with the base the device runs switches the product without a
//     restart: the running product's preinst copies the databases, the new
//     one is merged, its postinst restarts the services, and the boot health
//     check's baseline judges it (/usr/lib/a-nas-image/health); one that
//     breaks a check goes back.
//   - A release with a new base has systemd-sysupdate write it into the free
//     slot and waits for a restart. Products links each base to the product
//     that runs on it, and a-nas-product-select.service links the booted
//     base's before the extensions merge: a base that fails its boot check
//     three times is left by systemd-boot, and the base before starts with
//     its own product.
//
// The update files are read from Updates, where a push leaves them, or
// downloaded there from Source.
type ImageService struct {
	// Service is the state, commands, staging directory, database copies,
	// clock and disk space, as on a device installed with apt.
	Service
	// Source is the network address of the architecture's update files
	// (ANAS_UPDATE_URL); "" reads what a push left in Updates.
	Source string
	// Updates is the directory of update files the device installs from,
	// which systemd-sysupdate reads for the base.
	Updates string
	// Sysext keeps the product's versions; Extensions is what
	// systemd-sysext merges: the link to the product, and the content.
	Sysext     string
	Extensions string
	// Products links each base to the product that runs on it.
	Products string
	// ESP is the EFI system partition, whose EFI/Linux holds a UKI per base.
	ESP string
	// OSRelease and ProductRelease are the release files of the base and of
	// the product merged over it.
	OSRelease      string
	ProductRelease string
	// Health is the boot health check, Sysupdate and Pull systemd's tools,
	// Release the directory of the merged product's preinst and postinst.
	Health    string
	Sysupdate string
	Pull      string
	Release   string
	// BootID returns the running boot's ID.
	BootID func() string
	// HTTP downloads from Source.
	HTTP *http.Client
	// BootWait is how long a run after a restart waits for the boot to
	// complete; 0 is bootWait.
	BootWait time.Duration
}

// NewImageService is the service with the device's paths.
func NewImageService(service Service, source string) *ImageService {
	return &ImageService{
		Service: service, Source: source, Updates: imageUpdates, Sysext: imageSysext, Extensions: imageExtensions,
		Products: imageProducts, ESP: imageESP, OSRelease: imageOSRelease, ProductRelease: imageProductRelease,
		Health: imageHealth, Sysupdate: imageSysupdate, Pull: imagePull, Release: releaseScripts,
		BootID: func() string { return ReadBootID(bootIDFile) }, HTTP: &http.Client{},
	}
}

// ReadBootID reads a boot ID file, such as /proc/sys/kernel/random/boot_id; "" when it cannot.
func ReadBootID(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// IsImage reports whether the os-release file is the A-NAS image's.
func IsImage(osRelease string) bool {
	values, err := readEnvironmentFile(osRelease)
	return err == nil && values["IMAGE_ID"] == "a-nas"
}

// readEnvironmentFile reads KEY=VALUE lines, as os-release(5) and systemd's
// release files hold them; a value may be quoted.
func readEnvironmentFile(file string) (map[string]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			if value[0] == '"' {
				if unquoted, err := strconv.Unquote(value); err == nil {
					value = unquoted
				} else {
					value = value[1 : len(value)-1]
				}
			} else {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values, nil
}

// release is what scripts/build-image.sh publishes of one release in
// product/a-nas-release_SEQUENCE.json. Sequences are decimal strings, as
// systemd-sysupdate's versions are; Files are paths in the architecture's
// directory.
type release struct {
	Sequence     string                 `json:"sequence"`
	Version      string                 `json:"version"`
	Architecture string                 `json:"architecture"`
	HostID       string                 `json:"hostId"`
	Base         string                 `json:"base"`
	Content      map[string]string      `json:"content"`
	Files        map[string]releaseFile `json:"files"`
}

type releaseFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func productFile(sequence string) string { return "a-nas-product_" + sequence + ".raw" }
func releaseFileName(sequence string) string {
	return "a-nas-release_" + sequence + ".json"
}
func contentFile(role, sequence string) string {
	return "a-nas-content-" + role + "_" + sequence + ".raw"
}

// productPath is where release r's product is among the update files.
func (r release) productPath() string { return "product/" + productFile(r.Sequence) }

// contentPaths are r's content extensions among the update files, by role.
func (r release) contentPaths() []string {
	var paths []string
	for _, role := range slices.Sorted(maps.Keys(r.Content)) {
		paths = append(paths, "content-"+role+"/"+contentFile(role, r.Content[role]))
	}
	return paths
}

// basePaths are r's base image among the update files.
func (r release) basePaths() []string {
	return []string{"os/a-nas_" + r.Base + ".usr.raw.xz", "os/a-nas_" + r.Base + ".efi"}
}

// sequenceLess compares two decimal sequences.
func sequenceLess(a, b string) bool {
	x, errA := strconv.ParseUint(a, 10, 64)
	y, errB := strconv.ParseUint(b, 10, 64)
	if errA != nil || errB != nil {
		return a < b
	}
	return x < y
}

// Run carries out a request, as Service.Run does on a device installed
// with apt. A restart since a new base was written is settled first.
func (s *ImageService) Run(ctx context.Context, request Request) error {
	state, err := s.Store.Load()
	if err != nil {
		return err
	}
	if request.By == "" {
		request.By = ByAutomatic
	}
	if state.Image == nil {
		state.Image = &Image{}
	}
	if state.Unsettled(s.BootID()) && request.Kind != RequestBoot {
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
	case RequestRestart:
		err = s.restart(ctx, &state)
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

// tick takes a push, checks when a day has passed or the push brought
// something, and downloads when the settings say so.
func (s *ImageService) tick(ctx context.Context, state *State) error {
	if state.Phase.Busy() || state.Phase == PhaseRestartPending {
		return nil
	}
	pushed, err := s.syncPush(state)
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

// syncPush takes a push into Updates, as Service.syncStaging does into the
// local apt source. A device that downloads from Source takes no push.
func (s *ImageService) syncPush(state *State) (bool, error) {
	if s.Staging == "" || s.Source != "" {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(s.Staging, "product")); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	result, err := SyncImage(s.Staging, s.Updates, func(need int64) error {
		missing, err := s.missingSpace(filepath.Dir(s.Updates), need)
		if err != nil {
			return fmt.Errorf("read free space for the pushed update files: %w", err)
		}
		if missing > 0 {
			return &spaceError{missing: missing}
		}
		return nil
	})
	if err != nil {
		problem := &Problem{Code: ProblemStaging, Detail: err.Error()}
		if space, ok := errors.AsType[*spaceError](err); ok {
			problem = &Problem{Code: ProblemDiskSpace, Detail: strconv.FormatInt(space.missing, 10)}
		}
		if result.Files == 0 && result.Removed == 0 && (state.Phase == PhaseAvailable || state.Phase == PhaseReady) {
			s.recordCheckFailed(state, problem)
		} else {
			s.checkFailed(state, problem)
		}
		return false, err
	}
	return result.Files > 0 || result.Removed > 0, nil
}

// running reads what the device runs: the base's sequence and what the
// products it accepts need from it, and the merged product's version and
// sequence.
type running struct {
	base, hostID, version, product string
}

func (s *ImageService) running() (running, *Problem) {
	base, err := readEnvironmentFile(s.OSRelease)
	if err != nil {
		return running{}, &Problem{Code: ProblemNotInstalled, Detail: err.Error()}
	}
	product, err := readEnvironmentFile(s.ProductRelease)
	if err != nil {
		return running{base: base["IMAGE_VERSION"], hostID: base["A_NAS_HOST_ID"]},
			&Problem{Code: ProblemNotInstalled, Detail: "no A-NAS product is merged: " + err.Error()}
	}
	now := running{
		base: base["IMAGE_VERSION"], hostID: base["A_NAS_HOST_ID"],
		version: product["A_NAS_VERSION"], product: product["A_NAS_SEQUENCE"],
	}
	if now.product == "" {
		// A product built before its release file named its sequence: the
		// link's target names it.
		target, _ := os.Readlink(filepath.Join(s.Extensions, "a-nas-product.raw"))
		sequence, _ := strings.CutPrefix(filepath.Base(target), "a-nas-product_")
		now.product = strings.TrimSuffix(sequence, ".raw")
	}
	return now, nil
}

// check reads the signed list of releases and finds the candidate: the
// newest release, when it is newer than the product that runs and was not
// rejected.
func (s *ImageService) check(ctx context.Context, state *State, takePush bool) error {
	if state.Phase.Busy() || state.Phase == PhaseRestartPending {
		return nil
	}
	if takePush {
		if _, err := s.syncPush(state); err != nil {
			state.CheckedAt = s.now()
			return nil
		}
	}
	state.CheckedAt = s.now()
	now, problem := s.running()
	if problem != nil {
		s.checkFailed(state, problem)
		return nil
	}
	img := state.Image
	state.Current, img.Base, img.Product = now.version, now.base, now.product
	if img.Content == nil {
		img.Content = s.mergedContent()
	}
	if state.Previous != "" && !s.previousThere(img) {
		state.Previous, img.PreviousProduct, img.PreviousBase, img.PreviousContent = "", "", "", nil
	}
	sums, problem := s.index(ctx)
	if problem != nil {
		s.checkFailed(state, problem)
		return nil
	}
	newest := ""
	for name := range sums {
		sequence, ok := strings.CutPrefix(name, "a-nas-release_")
		if sequence, ok = strings.CutSuffix(sequence, ".json"); ok && (newest == "" || sequenceLess(newest, sequence)) {
			newest = sequence
		}
	}
	var candidate release
	if newest != "" && sequenceLess(img.Product, newest) {
		candidate, problem = s.release(ctx, sums, newest)
		if problem != nil {
			s.checkFailed(state, problem)
			return nil
		}
	}
	if candidate.Version == "" || slices.Contains(state.Rejected, candidate.Version) {
		state.LastCheckProblem = nil
		state.Candidate, state.DownloadBytes, state.DownloadedBytes, img.Candidate, img.Restart = "", 0, 0, "", false
		if state.Phase != PhaseRolledBack ||
			(state.Problem != nil && state.Problem.Code == ProblemRollback && s.assess(ctx) == nil) {
			state.Phase, state.Problem = PhaseUpToDate, nil
		}
		return nil
	}
	missing, problem := s.missing(candidate, img)
	if problem != nil {
		s.checkFailed(state, problem)
		return nil
	}
	state.Candidate, state.Problem, state.LastCheckProblem = candidate.Version, nil, nil
	img.Candidate, img.Restart = candidate.Sequence, candidate.Base != img.Base
	state.DownloadBytes, state.DownloadedBytes = 0, 0
	for _, name := range missing {
		state.DownloadBytes += candidate.Files[name].Size
	}
	if state.DownloadBytes == 0 {
		state.Phase = PhaseReady
	} else {
		state.Phase = PhaseAvailable
	}
	return nil
}

// mergedContent lists the content extensions in Extensions.
func (s *ImageService) mergedContent() []string {
	entries, err := os.ReadDir(s.Extensions)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if name := entry.Name(); strings.HasPrefix(name, "a-nas-content-") && strings.HasSuffix(name, ".raw") {
			names = append(names, name)
		}
	}
	return names
}

// previousThere reports whether "back to the version before" still has what
// it goes back to: the product, and the base it runs on in the other slot.
func (s *ImageService) previousThere(img *Image) bool {
	if img.PreviousProduct == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(s.Sysext, productFile(img.PreviousProduct))); err != nil {
		return false
	}
	if img.PreviousBase == "" || img.PreviousBase == img.Base {
		return true
	}
	tries := s.ukiTries(img.PreviousBase)
	return tries == ukiBlessed || tries == ukiTrying
}

// index refreshes and checks the product directory's SHA256SUMS: from
// Source, it is downloaded with its signature into Updates first.
// systemd-pull checks the signature against the keys of the A-NAS archive
// (/usr/lib/systemd/import-pubring.gpg), as systemd-sysupdate does.
func (s *ImageService) index(ctx context.Context) (map[string]string, *Problem) {
	if s.Source != "" {
		temporary, err := s.temporary(".index-")
		if err != nil {
			return nil, &Problem{Code: ProblemUnreachable, Detail: err.Error()}
		}
		defer func() { _ = os.RemoveAll(temporary) }()
		for _, name := range []string{"SHA256SUMS", "SHA256SUMS.gpg"} {
			if err := s.get(ctx, "product/"+name, filepath.Join(temporary, name), 0, nil); err != nil {
				return nil, &Problem{Code: ProblemUnreachable, Detail: err.Error()}
			}
		}
		if _, problem := s.verifiedSums(ctx, temporary); problem != nil {
			return nil, problem
		}
		for _, name := range []string{"SHA256SUMS", "SHA256SUMS.gpg"} {
			if err := moveInto(filepath.Join(temporary, name), filepath.Join(s.Updates, "product", name)); err != nil {
				return nil, &Problem{Code: ProblemDownload, Detail: err.Error()}
			}
		}
	}
	return s.verifiedSums(ctx, filepath.Join(s.Updates, "product"))
}

// verifiedSums checks the SHA256SUMS in dir against its signature and reads
// it: file name to SHA-256.
func (s *ImageService) verifiedSums(ctx context.Context, dir string) (map[string]string, *Problem) {
	if _, err := os.Stat(filepath.Join(dir, "SHA256SUMS")); err != nil {
		return nil, &Problem{Code: ProblemUnreachable, Detail: "no update files: " + err.Error()}
	}
	temporary, err := s.temporary(".sums-")
	if err != nil {
		return nil, &Problem{Code: ProblemUnreachable, Detail: err.Error()}
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	checked := filepath.Join(temporary, "SHA256SUMS")
	output, err := s.Commands.Run(ctx, s.Pull, "raw", "--direct", "--verify=signature", "--sync=no",
		"file://"+filepath.Join(dir, "SHA256SUMS"), checked)
	if err != nil {
		code := ProblemUnreachable
		if bytes.Contains(output, []byte("Signature verification failed")) || bytes.Contains(output, []byte("signature file")) ||
			bytes.Contains(output, []byte("DOWNLOAD INVALID")) {
			code = ProblemSignature
		}
		return nil, &Problem{Code: code, Detail: lastLines(string(output), 3)}
	}
	data, err := os.ReadFile(checked)
	if err != nil {
		return nil, &Problem{Code: ProblemUnreachable, Detail: err.Error()}
	}
	sums := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		sum, name, ok := strings.Cut(scanner.Text(), " ")
		if !ok || len(sum) != sha256.Size*2 {
			continue
		}
		name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
		if name != "" && !strings.Contains(name, "/") {
			sums[name] = strings.ToLower(sum)
		}
	}
	return sums, nil
}

// release reads release sequence's file, from Source into Updates first,
// and checks it against the signed SHA256SUMS.
func (s *ImageService) release(ctx context.Context, sums map[string]string, sequence string) (release, *Problem) {
	name := releaseFileName(sequence)
	target := filepath.Join(s.Updates, "product", name)
	file := releaseFile{SHA256: sums[name]}
	if s.Source != "" && !matches(target, file) {
		if err := s.get(ctx, "product/"+name, target, 0, nil); err != nil {
			return release{}, &Problem{Code: ProblemUnreachable, Detail: err.Error()}
		}
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return release{}, &Problem{Code: ProblemStaging, Detail: err.Error()}
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != file.SHA256 {
		return release{}, &Problem{Code: ProblemSignature, Detail: name + " does not match the signed SHA256SUMS"}
	}
	var r release
	if err := json.Unmarshal(data, &r); err != nil {
		return release{}, &Problem{Code: ProblemStaging, Detail: name + ": " + err.Error()}
	}
	if r.Sequence != sequence || r.Version == "" || r.Base == "" {
		return release{}, &Problem{Code: ProblemStaging, Detail: name + " names no release"}
	}
	if architecture := systemdArchitecture(); r.Architecture != architecture {
		return release{}, &Problem{Code: ProblemStaging, Detail: name + " is for " + r.Architecture + ", not " + architecture}
	}
	return r, nil
}

// matches reports whether the file at target has file's size, when known,
// and SHA-256.
func matches(target string, file releaseFile) bool {
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || (file.Size > 0 && info.Size() != file.Size) {
		return false
	}
	sum, err := fileSHA256(target)
	return err == nil && sum == file.SHA256
}

func fileSHA256(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// needed lists the update files installing r takes: the base when it is not
// the one the device runs, the product, and the content the device lacks.
func (s *ImageService) needed(r release, img *Image) []string {
	var names []string
	if r.Base != img.Base {
		names = append(names, r.basePaths()...)
	}
	names = append(names, r.productPath())
	for _, name := range r.contentPaths() {
		if _, err := os.Stat(filepath.Join(s.Extensions, path.Base(name))); err != nil {
			names = append(names, name)
		}
	}
	return names
}

// missing lists the update files installing r takes that Updates lacks: a
// push must have brought them all; a download fetches them.
func (s *ImageService) missing(r release, img *Image) ([]string, *Problem) {
	var missing []string
	for _, name := range s.needed(r, img) {
		file, ok := r.Files[name]
		if !ok {
			return nil, &Problem{Code: ProblemStaging, Detail: releaseFileName(r.Sequence) + " does not list " + name}
		}
		if info, err := os.Stat(filepath.Join(s.Updates, filepath.FromSlash(name))); err != nil || info.Size() != file.Size {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 && s.Source == "" {
		return nil, &Problem{Code: ProblemStaging, Detail: "the push lacks " + strings.Join(missing, ", ")}
	}
	return missing, nil
}

// candidateRelease reads the candidate's release file again, checked.
func (s *ImageService) candidateRelease(ctx context.Context, state *State) (release, *Problem) {
	sums, problem := s.verifiedSums(ctx, filepath.Join(s.Updates, "product"))
	if problem != nil {
		return release{}, problem
	}
	r, problem := s.release(ctx, sums, state.Image.Candidate)
	if problem == nil && r.Version != state.Candidate {
		problem = &Problem{Code: ProblemStaging, Detail: "the release file changed since the check"}
	}
	return r, problem
}

// download fetches the candidate's update files from Source into Updates,
// each checked against the release file's SHA-256, and the base's signed
// SHA256SUMS, which systemd-sysupdate checks. Files of other releases go
// first, so that downloads do not pile up.
func (s *ImageService) download(ctx context.Context, state *State) error {
	if state.Phase != PhaseAvailable || state.Image.Candidate == "" || s.Source == "" {
		return nil
	}
	sums, problem := s.verifiedSums(ctx, filepath.Join(s.Updates, "product"))
	var r release
	if problem == nil {
		r, problem = s.release(ctx, sums, state.Image.Candidate)
	}
	if problem != nil {
		state.Problem = problem
		return nil
	}
	missing, _ := s.missing(r, state.Image)
	keep := map[string]bool{}
	for _, name := range s.needed(r, state.Image) {
		keep[name] = true
	}
	s.pruneUpdates(keep)
	var need int64
	for _, name := range missing {
		need += r.Files[name].Size - partialSize(filepath.Join(s.Updates, filepath.FromSlash(name)))
	}
	if !s.enoughSpace(state, need) {
		return nil
	}
	state.Phase, state.StartedAt, state.DownloadedBytes = PhaseDownloading, s.now(), 0
	for _, name := range s.needed(r, state.Image) {
		if !slices.Contains(missing, name) {
			continue
		}
		state.DownloadedBytes += partialSize(filepath.Join(s.Updates, filepath.FromSlash(name)))
	}
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	if r.Base != state.Image.Base {
		for _, name := range []string{"os/SHA256SUMS", "os/SHA256SUMS.gpg"} {
			if err := s.get(ctx, name, filepath.Join(s.Updates, filepath.FromSlash(name)), 0, nil); err != nil {
				return s.downloadFailed(state, err)
			}
		}
	}
	every := s.ProgressEvery
	if every <= 0 {
		every = progressEvery
	}
	saved := s.now()
	done := state.DownloadedBytes
	for _, name := range missing {
		file := r.Files[name]
		target := filepath.Join(s.Updates, filepath.FromSlash(name))
		start := partialSize(target)
		err := s.get(ctx, name, target, file.Size, func(have int64) {
			state.DownloadedBytes = done + have - start
			if s.now().Sub(saved) >= every {
				saved = s.now()
				_ = s.Store.Save(*state) // only the page's progress
			}
		})
		if err == nil && !matches(target, file) {
			_ = os.Remove(target)
			err = fmt.Errorf("%s does not match the release file", name)
		}
		if err != nil {
			return s.downloadFailed(state, err)
		}
		done += file.Size - start
	}
	state.Phase, state.DownloadBytes, state.DownloadedBytes, state.Problem = PhaseReady, done, done, nil
	state.Record(Record{At: s.now(), Kind: RecordDownloaded, Version: state.Candidate, By: ByAutomatic})
	return nil
}

func (s *ImageService) downloadFailed(state *State, err error) error {
	state.Phase = PhaseAvailable
	state.Problem = &Problem{Code: ProblemDownload, Detail: err.Error()}
	return nil
}

// partialSize is how much of a download is on disk: the whole file, or the
// part in NAME.partial.
func partialSize(target string) int64 {
	for _, name := range []string{target, target + ".partial"} {
		if info, err := os.Stat(name); err == nil {
			return info.Size()
		}
	}
	return 0
}

// pruneUpdates removes the update files a download left that keep does not
// name; the signed lists and release files stay.
func (s *ImageService) pruneUpdates(keep map[string]bool) {
	entries, err := os.ReadDir(s.Updates)
	if err != nil {
		return
	}
	for _, dir := range entries {
		if !dir.IsDir() || !imageLayout.top(dir.Name()) {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(s.Updates, dir.Name()))
		for _, file := range files {
			name := dir.Name() + "/" + strings.TrimSuffix(file.Name(), ".partial")
			base := path.Base(name)
			if keep[name] || base == "SHA256SUMS" || base == "SHA256SUMS.gpg" || strings.HasSuffix(base, ".json") {
				continue
			}
			_ = os.Remove(filepath.Join(s.Updates, dir.Name(), file.Name()))
		}
	}
}

// get downloads name from Source to target. A file of size bytes resumes
// from what NAME.partial holds; progress, when set, hears how many bytes of
// it are on disk.
func (s *ImageService) get(ctx context.Context, name, target string, size int64, progress func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	partial := target + ".partial"
	offset := int64(0)
	if size > 0 {
		if info, err := os.Stat(target); err == nil && info.Size() == size {
			return nil
		}
		if info, err := os.Stat(partial); err == nil && info.Size() < size {
			offset = info.Size()
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(s.Source, "/")+"/"+name, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	response, err := s.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer func() { _ = response.Body.Close() }()
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	switch {
	case response.StatusCode == http.StatusPartialContent && offset > 0:
		flags = os.O_WRONLY | os.O_APPEND
	case response.StatusCode == http.StatusOK:
		offset = 0
	default:
		return fmt.Errorf("download %s: %s", name, response.Status)
	}
	//nolint:gosec // update files are not secret; systemd-sysupdate reads them
	out, err := os.OpenFile(partial, flags, 0o644)
	if err != nil {
		return err
	}
	var reader io.Reader = response.Body
	if size > 0 {
		reader = io.LimitReader(response.Body, size-offset)
	} else {
		reader = io.LimitReader(response.Body, 1<<20) // a signed list or its signature
	}
	written := offset
	_, err = io.Copy(out, &progressReader{reader: reader, add: func(n int) {
		written += int64(n)
		if progress != nil {
			progress(written)
		}
	}})
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if size > 0 && written != size {
		return fmt.Errorf("download %s: got %d of %d bytes", name, written, size)
	}
	return os.Rename(partial, target)
}

type progressReader struct {
	reader io.Reader
	add    func(int)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.add(n)
	}
	return n, err
}

// moveInto replaces target with the file from, across directories.
func moveInto(from, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	temporary := target + ".new"
	//nolint:gosec // signed lists, not secret
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, target)
}

// install installs the candidate: its content and product are put in place,
// which changes nothing that runs; then the product is switched, or the base
// written for a restart.
func (s *ImageService) install(ctx context.Context, state *State, by string) error {
	if state.Phase != PhaseReady || state.Image.Candidate == "" {
		return nil
	}
	r, problem := s.candidateRelease(ctx, state)
	if problem != nil {
		state.Problem = problem
		return nil
	}
	img := state.Image
	var need int64
	for _, name := range append(r.contentPaths(), r.productPath()) {
		if _, err := os.Stat(filepath.Join(s.Extensions, path.Base(name))); err != nil {
			need += r.Files[name].Size
		}
	}
	if !s.enoughSpace(state, need) {
		return nil
	}
	state.Phase, state.Target, state.From, state.By = PhaseInstalling, r.Version, state.Current, by
	state.StartedAt, state.Problem, state.BackupName = s.now(), nil, ""
	img.TargetProduct, img.TargetBase, img.FromBase, img.BootID = r.Sequence, r.Base, img.Base, ""
	img.TargetContent = nil
	for _, name := range r.contentPaths() {
		img.TargetContent = append(img.TargetContent, path.Base(name))
	}
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	if problem := s.place(r); problem != nil {
		s.notInstalled(state, problem)
		return nil
	}
	if r.Base != img.Base {
		return s.writeBase(ctx, state, by)
	}
	// What fails now does not count against the product switched to.
	_, _ = s.Commands.Run(ctx, s.Health, "record")
	if problem := s.copyDatabases(ctx, state); problem != nil {
		s.notInstalled(state, problem)
		return nil
	}
	if problem := s.useProduct(ctx, img.Base, img.TargetProduct, state.From); problem != nil {
		return s.goBack(ctx, state, problem)
	}
	return s.verify(ctx, state)
}

// place copies the candidate's content into Extensions and its product into
// Sysext, each checked against the release file. Neither is linked: content
// is merged beside what runs, and only at the next merge.
func (s *ImageService) place(r release) *Problem {
	copies := map[string]string{r.productPath(): filepath.Join(s.Sysext, productFile(r.Sequence))}
	for _, name := range r.contentPaths() {
		copies[name] = filepath.Join(s.Extensions, path.Base(name))
	}
	for _, name := range slices.Sorted(maps.Keys(copies)) {
		target := copies[name]
		if matches(target, r.Files[name]) {
			continue
		}
		if err := checkedCopy(filepath.Join(s.Updates, filepath.FromSlash(name)), target, r.Files[name]); err != nil {
			return &Problem{Code: ProblemInstall, Detail: err.Error()}
		}
	}
	return nil
}

// checkedCopy copies from to target through a temporary file that
// systemd-sysext does not read, and keeps it only when its SHA-256 is the
// release file's.
func checkedCopy(from, target string, file releaseFile) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(temporary, hash), source)
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
		err = fmt.Errorf("%s does not match the release file", from)
	}
	if err == nil {
		err = temporary.Chmod(0o644)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary.Name(), target)
	}
	return err
}

// copyDatabases runs the running product's preinst, which copies the
// databases as before an apt install.
func (s *ImageService) copyDatabases(ctx context.Context, state *State) *Problem {
	output, err := s.Commands.Run(ctx, filepath.Join(s.Release, "preinst"), "upgrade", state.From)
	if err != nil {
		return &Problem{Code: ProblemInstall, Detail: "copy the databases: " + lastLines(string(output), 3)}
	}
	state.BackupName = s.backupOf(state.From, state.StartedAt)
	return nil
}

// link points name at target, replacing it whole.
func link(target, name string) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".new")
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	return os.Rename(temporary, name)
}

// productFor makes product the one that runs on base: now, and at each boot
// of that base (a-nas-product-select.service).
func (s *ImageService) productFor(base, product string) error {
	return link(filepath.Join(s.Sysext, productFile(product)), filepath.Join(s.Products, base))
}

// useProduct switches the running base to product: it is linked, merged,
// and its postinst restarts the services in order. left is the version the
// switch leaves, which postinst is told, as dpkg tells it.
func (s *ImageService) useProduct(ctx context.Context, base, product, left string) *Problem {
	if err := s.productFor(base, product); err != nil {
		return &Problem{Code: ProblemInstall, Detail: err.Error()}
	}
	if err := link(filepath.Join(s.Sysext, productFile(product)), filepath.Join(s.Extensions, "a-nas-product.raw")); err != nil {
		return &Problem{Code: ProblemInstall, Detail: err.Error()}
	}
	if output, err := s.Commands.Run(ctx, "systemd-sysext", "refresh"); err != nil {
		return &Problem{Code: ProblemInstall, Detail: "systemd-sysext refresh: " + lastLines(string(output), 3)}
	}
	if output, err := s.Commands.Run(ctx, filepath.Join(s.Release, "postinst"), "configure", left); err != nil {
		return &Problem{Code: ProblemInstall, Detail: "postinst: " + lastLines(string(output), 5)}
	}
	return nil
}

// assess runs the boot health check's judgement: a check that passed before
// the switch and fails now (deploy/image's health). The baseline is what
// failed when the last install began, or at the last boot that systemd-boot
// did not count.
func (s *ImageService) assess(ctx context.Context) *Problem {
	output, err := s.Commands.Run(ctx, s.Health, "assess")
	if err == nil {
		return nil
	}
	var lines []string
	for line := range strings.Lines(string(output)) {
		if strings.HasPrefix(line, "FAIL ") || strings.HasPrefix(line, "a-nas-health: ") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	if len(lines) == 0 {
		lines = []string{lastLines(string(output), 3)}
	}
	return &Problem{Code: ProblemCheck, Detail: strings.Join(lines, "\n")}
}

// verify judges the product just switched to: it passes, or the device goes
// back to the product before.
func (s *ImageService) verify(ctx context.Context, state *State) error {
	if problem := s.assess(ctx); problem != nil {
		return s.goBack(ctx, state, problem)
	}
	s.installed(state)
	return nil
}

// installed records the release as installed: what ran before is what
// "back to the version before" goes back to.
func (s *ImageService) installed(state *State) {
	img := state.Image
	state.Record(Record{At: s.now(), Kind: RecordUpdated, Version: state.Target, From: state.From, By: state.By})
	state.Previous, state.Current, state.InstalledAt = state.From, state.Target, s.now()
	img.PreviousProduct, img.PreviousBase, img.PreviousContent = img.Product, img.FromBase, img.Content
	img.Product, img.Base, img.Content = img.TargetProduct, img.TargetBase, img.TargetContent
	state.Phase, state.Candidate, state.DownloadBytes, state.DownloadedBytes = PhaseUpToDate, "", 0, 0
	img.Candidate, img.Restart = "", false
	s.clearOperation(state)
	s.vacuum(img)
}

func (s *ImageService) clearOperation(state *State) {
	img := state.Image
	state.Target, state.From, state.By, state.BackupName, state.DpkgBefore, state.Reverting = "", "", "", "", "", false
	img.TargetProduct, img.TargetBase, img.TargetContent, img.FromBase, img.BootID = "", "", nil, "", ""
}

// notInstalled ends an install that stopped before it changed what runs: the
// version is offered again.
func (s *ImageService) notInstalled(state *State, problem *Problem) {
	state.Record(Record{At: s.now(), Kind: RecordFailed, Version: state.Target, From: state.From, By: state.By, Problem: problem})
	state.Phase, state.Problem = PhaseReady, problem
	s.clearOperation(state)
}

// goBack switches back to the product the install started from after its
// switch or check failed, and restores the database copies when the product
// before fails its check too.
func (s *ImageService) goBack(ctx context.Context, state *State, cause *Problem) error {
	if cause == nil {
		cause = &Problem{Code: ProblemInstall, Detail: "the install was interrupted"}
	}
	img := state.Image
	state.Phase, state.Problem, state.Reverting = PhaseRollingBack, cause, false
	state.Reject(state.Target)
	if state.BackupName == "" {
		state.BackupName = s.backupOf(state.From, state.StartedAt)
	}
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	// A service of the failed product that kept failing has used up its
	// start limit, and the product before's postinst could not restart it.
	_, _ = s.Commands.Run(ctx, "systemctl", "reset-failed")
	kind, problem := RecordRolledBack, cause
	if failure := s.useProduct(ctx, img.Base, img.Product, state.Target); failure != nil {
		kind, problem = RecordFailed, &Problem{Code: ProblemRollback, Detail: cause.Detail + "\n" + failure.Detail}
	} else if failure := s.assess(ctx); failure != nil {
		if restored := s.restoreDatabases(ctx, state.BackupName); restored != nil || s.assess(ctx) != nil {
			kind, problem = RecordFailed, &Problem{Code: ProblemRollback, Detail: cause.Detail + "\n" + failure.Detail}
		}
	}
	s.rolledBack(state, kind, problem)
	return nil
}

// rolledBack records an install the device went back from.
func (s *ImageService) rolledBack(state *State, kind string, problem *Problem) {
	state.Record(Record{At: s.now(), Kind: kind, Version: state.Target, From: state.From, By: state.By, Problem: problem})
	state.Current, state.Phase, state.Problem = state.From, PhaseRolledBack, problem
	state.Candidate, state.DownloadBytes, state.DownloadedBytes = "", 0, 0
	state.Image.Candidate, state.Image.Restart = "", false
	s.clearOperation(state)
}

// writeBase writes the candidate's base into the free slot and links each
// base to its product: the new base to the new product, the running base to
// the running product, which systemd-boot goes back to with it. The
// databases are copied first, with the boot health check's baseline. A
// restart starts the new base; one in the install window follows at once.
func (s *ImageService) writeBase(ctx context.Context, state *State, by string) error {
	img := state.Image
	_, _ = s.Commands.Run(ctx, s.Health, "record")
	if problem := s.copyDatabases(ctx, state); problem != nil {
		s.notInstalled(state, problem)
		return nil
	}
	for base, product := range map[string]string{img.Base: img.Product, img.TargetBase: img.TargetProduct} {
		if err := s.productFor(base, product); err != nil {
			s.notInstalled(state, &Problem{Code: ProblemInstall, Detail: err.Error()})
			return nil
		}
	}
	output, err := s.Commands.Run(ctx, s.Sysupdate, "update", img.TargetBase)
	if err != nil || s.ukiTries(img.TargetBase) == ukiMissing {
		// The UKI is written last: without it, systemd-boot starts what runs.
		s.notInstalled(state, &Problem{Code: ProblemInstall, Detail: "systemd-sysupdate: " + lastLines(string(output), 5)})
		return nil
	}
	state.Phase, img.BootID = PhaseRestartPending, s.BootID()
	if by == ByAutomatic {
		return s.restart(ctx, state)
	}
	return nil
}

// restart restarts into the base an install wrote.
func (s *ImageService) restart(ctx context.Context, state *State) error {
	if state.Phase != PhaseRestartPending {
		return nil
	}
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	if output, err := s.Commands.Run(ctx, "systemctl", "reboot"); err != nil {
		return fmt.Errorf("restart: %w: %s", err, lastLines(string(output), 3))
	}
	return nil
}

// UKI states of a base in EFI/Linux, by the boot counter in its name
// (https://uapi-group.org/specifications/specs/boot_loader_specification/#boot-counting).
const (
	ukiMissing = iota
	ukiBlessed // no counter: passed its boot check
	ukiTrying  // tries left
	ukiBad     // no tries left: systemd-boot starts it only when nothing else is left
)

var ukiCounter = regexp.MustCompile(`^\+(\d+)(-\d+)?$`)

// ukiTries reads the counter in the name of base's UKI.
func (s *ImageService) ukiTries(base string) int {
	matches, _ := filepath.Glob(filepath.Join(s.ESP, "EFI", "Linux", "a-nas_"+base+"*.efi"))
	for _, name := range matches {
		counter := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), "a-nas_"+base), ".efi")
		if counter == "" {
			return ukiBlessed
		}
		if m := ukiCounter.FindStringSubmatch(counter); m != nil {
			if m[1] == "0" {
				return ukiBad
			}
			return ukiTrying
		}
	}
	return ukiMissing
}

// retire marks base's UKI as one that failed every try, as systemd-boot
// does, so that it starts the base before instead.
func (s *ImageService) retire(base string) error {
	matches, _ := filepath.Glob(filepath.Join(s.ESP, "EFI", "Linux", "a-nas_"+base+"*.efi"))
	for _, name := range matches {
		counter := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), "a-nas_"+base), ".efi")
		if counter != "" && !ukiCounter.MatchString(counter) {
			continue
		}
		done := "0"
		if m := ukiCounter.FindStringSubmatch(counter); m != nil && m[2] != "" {
			done = m[2][1:]
		}
		return os.Rename(name, filepath.Join(filepath.Dir(name), "a-nas_"+base+"+0-"+done+".efi"))
	}
	return fmt.Errorf("no UKI of the base %s", base)
}

// waitForBoot waits for boot-complete.target, which the boot health check
// holds back on a base's counted boots; one that fails restarts the device.
func (s *ImageService) waitForBoot(ctx context.Context) bool {
	wait := s.BootWait
	if wait <= 0 {
		wait = bootWait
	}
	deadline := s.now().Add(wait)
	for {
		output, _ := s.Commands.Run(ctx, "systemctl", "is-active", "boot-complete.target")
		if strings.TrimSpace(string(output)) == "active" {
			return true
		}
		if !s.now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Second):
		}
	}
}

// boot settles a restart since a base was written, and finishes an
// operation a power cut, reboot or kill stopped.
func (s *ImageService) boot(ctx context.Context, state *State) error {
	img := state.Image
	switch state.Phase {
	case PhaseDownloading:
		state.Phase = PhaseAvailable
		return nil
	case PhaseRestartPending:
		if img.BootID == s.BootID() {
			return nil
		}
		return s.afterRestart(ctx, state)
	case PhaseInstalling, PhaseRollingBack:
	default:
		return nil
	}
	now, problem := s.running()
	if problem != nil && state.Phase == PhaseInstalling {
		return s.goBack(ctx, state, problem)
	}
	switch {
	case state.Phase == PhaseInstalling && img.TargetBase != img.FromBase:
		// Stopped while the base was written, or before the restart was
		// saved as waiting.
		switch {
		case now.base == img.TargetBase:
			return s.afterRestart(ctx, state)
		case s.ukiTries(img.TargetBase) == ukiTrying:
			state.Phase, img.BootID = PhaseRestartPending, s.BootID()
			return nil
		case s.ukiTries(img.TargetBase) == ukiBad:
			return s.afterRestart(ctx, state)
		default:
			s.notInstalled(state, &Problem{Code: ProblemInstall, Detail: "the install was interrupted before it changed anything"})
			return nil
		}
	case state.Phase == PhaseInstalling && now.product == img.TargetProduct:
		return s.verify(ctx, state)
	case state.Phase == PhaseInstalling:
		s.notInstalled(state, &Problem{Code: ProblemInstall, Detail: "the install was interrupted before it changed anything"})
		return nil
	case state.Reverting:
		return s.reverting(ctx, state)
	}
	return s.goBack(ctx, state, state.Problem)
}

// afterRestart settles a restart since a base was written: on the new base,
// the install or revert is done once the boot completed; on the base
// before, systemd-boot went back to it, and the device is on the version
// before with its product.
func (s *ImageService) afterRestart(ctx context.Context, state *State) error {
	img := state.Image
	now, _ := s.running()
	if now.base == img.TargetBase {
		if !s.waitForBoot(ctx) {
			return nil // the boot health check is still running; the next run looks again
		}
		if now.product != img.TargetProduct {
			problem := &Problem{Code: ProblemInstall, Detail: "the base " + now.base + " started product " + now.product +
				", not " + img.TargetProduct}
			if state.Reverting {
				s.finishFailedRevert(state, problem)
				return nil
			}
			s.rolledBack(state, RecordFailed, &Problem{Code: ProblemRollback, Detail: problem.Detail})
			return nil
		}
		if state.Reverting {
			s.reverted(state)
		} else {
			s.installed(state)
		}
		return nil
	}
	if s.ukiTries(img.TargetBase) == ukiTrying {
		// Started by hand into the base before: the new one still waits.
		img.BootID = s.BootID()
		state.Phase = PhaseRestartPending
		return nil
	}
	if state.Reverting {
		s.finishFailedRevert(state, &Problem{Code: ProblemRollback, Detail: "the base " + img.TargetBase + " did not start"})
		return nil
	}
	cause := &Problem{Code: ProblemBoot, Detail: "the base " + img.TargetBase + " did not pass its boot check; systemd-boot went back to " + now.base}
	state.Reject(state.Target)
	if state.BackupName == "" {
		state.BackupName = s.backupOf(state.From, state.StartedAt)
	}
	// The product before started with the base before; the databases it
	// finds are what the failed tries left, which it takes back as after any
	// rollback, or which the copy replaces.
	kind, problem := RecordRolledBack, cause
	if now.product != img.Product {
		kind, problem = RecordFailed, &Problem{Code: ProblemRollback, Detail: cause.Detail + "\nthe base started product " + now.product}
	} else if failure := s.assess(ctx); failure != nil {
		if restored := s.restoreDatabases(ctx, state.BackupName); restored != nil || s.assess(ctx) != nil {
			kind, problem = RecordFailed, &Problem{Code: ProblemRollback, Detail: cause.Detail + "\n" + failure.Detail}
		}
	}
	s.rolledBack(state, kind, problem)
	return nil
}

// revert goes back to the version before the last install that passed, as
// the administrator asked; the version left is not offered again. On the
// same base the product switches back at once; otherwise the base the
// device leaves is retired and a restart starts the one before.
func (s *ImageService) revert(ctx context.Context, state *State, by string) error {
	img := state.Image
	if state.Phase.Busy() || state.Phase == PhaseRestartPending || state.Previous == "" {
		return nil
	}
	if !s.previousThere(img) {
		state.Previous, img.PreviousProduct, img.PreviousBase, img.PreviousContent = "", "", "", nil
		return nil
	}
	state.Phase, state.Reverting, state.Target, state.From, state.By = PhaseRollingBack, true, state.Previous, state.Current, by
	state.StartedAt, state.Problem, state.BackupName = s.now(), nil, ""
	img.TargetProduct, img.TargetBase, img.TargetContent = img.PreviousProduct, img.PreviousBase, img.PreviousContent
	img.FromBase, img.BootID = img.Base, ""
	if img.TargetBase == "" {
		img.TargetBase = img.Base
	}
	if err := s.Store.Save(*state); err != nil {
		return err
	}
	_, _ = s.Commands.Run(ctx, s.Health, "record")
	if problem := s.copyDatabases(ctx, state); problem != nil {
		state.Problem = problem
		s.finishFailedRevert(state, nil)
		return nil
	}
	if img.TargetBase != img.Base {
		if err := s.productFor(img.TargetBase, img.TargetProduct); err != nil {
			state.Problem = &Problem{Code: ProblemRollback, Detail: err.Error()}
			s.finishFailedRevert(state, nil)
			return nil
		}
		if err := s.retire(img.Base); err != nil {
			state.Problem = &Problem{Code: ProblemRollback, Detail: err.Error()}
			s.finishFailedRevert(state, nil)
			return nil
		}
		state.Phase, img.BootID = PhaseRestartPending, s.BootID()
		return nil
	}
	return s.reverting(ctx, state)
}

// reverting switches the product back on the running base: after revert, or
// in a run that finds a revert a power cut stopped.
func (s *ImageService) reverting(ctx context.Context, state *State) error {
	img := state.Image
	if state.Problem != nil {
		// The switch back failed before: restore what ran.
		failure := s.useProduct(ctx, img.Base, img.Product, state.Target)
		if failure == nil {
			failure = s.assess(ctx)
		}
		s.finishFailedRevert(state, failure)
		return nil
	}
	problem := s.useProduct(ctx, img.Base, img.TargetProduct, state.From)
	if problem == nil {
		problem = s.assess(ctx)
	}
	if problem != nil {
		state.Problem = problem
		if err := s.Store.Save(*state); err != nil {
			return err
		}
		return s.reverting(ctx, state)
	}
	s.reverted(state)
	return nil
}

// reverted records a revert that passed its check.
func (s *ImageService) reverted(state *State) {
	img := state.Image
	state.Reject(state.From)
	state.Record(Record{At: s.now(), Kind: RecordReverted, Version: state.Target, From: state.From, By: state.By})
	state.Current, state.Previous, state.InstalledAt, state.Phase = state.Target, "", s.now(), PhaseUpToDate
	img.Product, img.Base, img.Content = img.TargetProduct, img.TargetBase, img.TargetContent
	img.PreviousProduct, img.PreviousBase, img.PreviousContent = "", "", nil
	s.clearOperation(state)
}

// finishFailedRevert records a revert that did not happen; failure, when
// set, is what went wrong going back to what ran.
func (s *ImageService) finishFailedRevert(state *State, failure *Problem) {
	if state.Problem == nil {
		state.Problem = failure
		failure = nil
	}
	state.Phase = PhaseUpToDate
	if failure != nil {
		state.Problem = &Problem{Code: ProblemRollback, Detail: state.Problem.Detail + "\n" + failure.Detail}
		state.Phase = PhaseRolledBack
	}
	state.Record(Record{At: s.now(), Kind: RecordFailed, Version: state.Target, From: state.From, By: state.By, Problem: state.Problem})
	s.clearOperation(state)
}

// vacuum removes the products and content that neither the version that
// runs nor the one before uses, and the links of bases neither runs on.
func (s *ImageService) vacuum(img *Image) {
	products := map[string]bool{productFile(img.Product): true, productFile(img.PreviousProduct): true}
	if entries, err := os.ReadDir(s.Sysext); err == nil {
		for _, entry := range entries {
			if name := entry.Name(); strings.HasPrefix(name, "a-nas-product_") && strings.HasSuffix(name, ".raw") && !products[name] {
				_ = os.Remove(filepath.Join(s.Sysext, name))
			}
		}
	}
	content := map[string]bool{}
	for _, name := range append(slices.Clone(img.Content), img.PreviousContent...) {
		content[name] = true
	}
	for _, name := range s.mergedContent() {
		// A state that does not know what runs removes nothing.
		if len(content) > 0 && !content[name] {
			_ = os.Remove(filepath.Join(s.Extensions, name))
		}
	}
	if entries, err := os.ReadDir(s.Products); err == nil {
		for _, entry := range entries {
			if name := entry.Name(); name != img.Base && name != img.PreviousBase {
				_ = os.Remove(filepath.Join(s.Products, name))
			}
		}
	}
}

// systemdArchitecture is this program's architecture as systemd names it
// (the %a specifier), which names the directory of update files.
func systemdArchitecture() string {
	return architectures[runtime.GOARCH]
}

var architectures = map[string]string{"amd64": "x86-64", "arm64": "arm64"}

// temporary makes a directory of root's in the state directory, which the
// first run may not have made yet.
func (s *ImageService) temporary(prefix string) (string, error) {
	if err := os.MkdirAll(s.Store.Dir, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(s.Store.Dir, prefix)
}
