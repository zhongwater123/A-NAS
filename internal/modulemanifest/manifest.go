// Package modulemanifest names what A-NAS knows about a removable module
// (ADR 0037) without anything else of A-NAS: its ID, account, store and
// manifest. The Host Agent reads manifests with it to keep the modules'
// stores; it imports nothing but the standard library, so the root side's
// dependency closure stays small.
package modulemanifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	// SocketDir holds each installed module's API socket, <id>.sock, which
	// its socket unit creates as root for the SocketGroup.
	SocketDir = "/run/a-nas-modules"
	// SocketGroup may connect to module sockets; only the Product Service
	// is in it, so modules cannot reach one another.
	SocketGroup = "a-nas-modules"
	// SessionSocket is the Host Agent's session lookup for modules, open to
	// the SessionGroup that only module units grant.
	SessionSocket = "/run/a-nas-module-sessions/sessions.sock"
	SessionGroup  = "a-nas-module-sessions"
	// Dir holds a root-owned <id>.json for each installed module.
	Dir = "/etc/a-nas/modules"
	// Module accounts have fixed IDs in their own range, apart from the
	// built-in service identities from 31000.
	FirstUID = 31100
	LastUID  = 31199
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,30}$`)

// ValidID reports whether id can name a module: 2–31 lowercase letters and
// digits, starting with a letter, so that it is safe in paths, unit and
// account names.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Account is the system account a module runs as.
func Account(id string) string { return "a-nas-" + id }

// StoreName is the module's subvolume at the top of the data volume.
func StoreName(id string) string { return "module-" + id }

// SocketPath is where the module's socket unit listens.
func SocketPath(dir, id string) string { return filepath.Join(dir, id+".sock") }

// Manifest is a module's /etc/a-nas/modules/<id>.json.
type Manifest struct {
	ID  string `json:"id"`
	UID int    `json:"uid"`
}

func (m Manifest) validate(file string) error {
	switch {
	case !ValidID(m.ID):
		return fmt.Errorf("module manifest %s: invalid id %q", file, m.ID)
	case file != m.ID+".json":
		return fmt.Errorf("module manifest %s: names module %q", file, m.ID)
	case m.UID < FirstUID || m.UID > LastUID:
		return fmt.Errorf("module manifest %s: uid %d is outside %d–%d", file, m.UID, FirstUID, LastUID)
	}
	return nil
}

// Read returns the valid manifests in dir, sorted by ID, and an
// error naming each one it skipped. A missing dir means no module.
func Read(dir string) ([]Manifest, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifests []Manifest
	var problems []error
	uids := map[int]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		var manifest Manifest
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&manifest); err != nil {
			problems = append(problems, fmt.Errorf("module manifest %s: %w", name, err))
			continue
		}
		if err := manifest.validate(name); err != nil {
			problems = append(problems, err)
			continue
		}
		if other, taken := uids[manifest.UID]; taken {
			problems = append(problems, fmt.Errorf("module manifest %s: uid %d is also %s's", name, manifest.UID, other))
			continue
		}
		uids[manifest.UID] = manifest.ID
		manifests = append(manifests, manifest)
	}
	slices.SortFunc(manifests, func(a, b Manifest) int { return strings.Compare(a.ID, b.ID) })
	return manifests, errors.Join(problems...)
}
