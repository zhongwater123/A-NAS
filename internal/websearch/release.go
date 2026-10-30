package websearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Release is what the installed release holds of SearXNG: its pin, and in
// its runtime the license, the source archive it was built from and the
// licenses of every package (scripts/build-search-runtime.sh). The settings
// page offers them, as AGPL-3.0 asks of whoever passes SearXNG on (ADR 0030).
type Release struct {
	dir string
}

// NewRelease reads the release's search directory, /opt/a-nas/current/search
// on the NAS.
func NewRelease(dir string) Release { return Release{dir: dir} }

// License is the open source notice of web search.
type License struct {
	Name     string    `json:"name"`
	Version  string    `json:"version"`
	Commit   string    `json:"commit"`
	License  string    `json:"license"`
	Packages []Package `json:"packages"`
	// Source tells whether this release can hand out SearXNG's source.
	Source bool `json:"source"`
}

// Package is a Python package in the runtime.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	License string `json:"license"`
}

// ErrNoRelease is a release without SearXNG's pin or runtime, as in development.
var ErrNoRelease = errors.New("this release carries no web search runtime")

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (r Release) pin() (struct{ Version, Commit, License string }, error) {
	var pin struct{ Version, Commit, License string }
	contents, err := os.ReadFile(filepath.Join(r.dir, "searxng.json"))
	if errors.Is(err, os.ErrNotExist) {
		return pin, ErrNoRelease
	}
	if err != nil {
		return pin, err
	}
	if err := json.Unmarshal(contents, &pin); err != nil {
		return pin, fmt.Errorf("read searxng.json: %w", err)
	}
	if !commitPattern.MatchString(pin.Commit) {
		return pin, fmt.Errorf("searxng.json names no commit")
	}
	return pin, nil
}

func (r Release) License() (License, error) {
	pin, err := r.pin()
	if err != nil {
		return License{}, err
	}
	// The manifest's license starts with the SPDX expression.
	expression, _, _ := strings.Cut(pin.License, " ")
	license := License{Name: "SearXNG", Version: pin.Version, Commit: pin.Commit, License: expression, Packages: []Package{}}
	contents, err := os.ReadFile(filepath.Join(r.dir, "runtime", "share", "packages.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return License{}, err
	default:
		if err := json.Unmarshal(contents, &license.Packages); err != nil {
			return License{}, fmt.Errorf("read packages.json: %w", err)
		}
	}
	_, err = os.Stat(r.sourcePath(pin.Commit))
	license.Source = err == nil
	return license, nil
}

// LicenseText is SearXNG's own LICENSE.
func (r Release) LicenseText() (string, error) {
	contents, err := os.ReadFile(filepath.Join(r.dir, "runtime", "share", "searxng", "LICENSE"))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoRelease
	}
	return string(contents), err
}

// Source opens the archive of SearXNG's source this release was built from,
// with the name to save it under.
func (r Release) Source() (*os.File, string, error) {
	pin, err := r.pin()
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(r.sourcePath(pin.Commit))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrNoRelease
	}
	return file, "searxng-" + pin.Commit + ".tar.gz", err
}

var secretLine = regexp.MustCompile(`(?m)^SEARXNG_SECRET=([0-9a-f]{64})$`)

// ReadSecret reads SearXNG's secret from its environment file, which the
// installer writes once (/etc/a-nas/search.env).
func ReadSecret(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("no secret file is configured")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	match := secretLine.FindSubmatch(contents)
	if match == nil {
		return nil, fmt.Errorf("%s holds no SEARXNG_SECRET of 64 hexadecimal digits", path)
	}
	return match[1], nil
}

func (r Release) sourcePath(commit string) string {
	return filepath.Join(r.dir, "runtime", "share", "searxng", "searxng-"+commit+".tar.gz")
}
