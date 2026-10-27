package files

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Agent空间 and its immediate children are policy directories managed by
// the Host Agent, not member-created entries. The same name elsewhere is
// ordinary user data. Contents below each member's directory remain editable.
func isAgentPolicyDirectory(root, spaceID, name string) bool {
	if spaceID != "space:shared" {
		return false
	}
	relative, err := filepath.Rel(root, name)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	return len(parts) <= 2 && strings.EqualFold(parts[0], "Agent空间")
}

// Names a user gives to new or renamed items must also work over SMB on
// Windows and macOS (docs/specs/basic-storage-and-sharing.md#名称规则).
// Existing names are left alone: an item created over SMB or before these
// rules keeps its name when it is moved, copied or restored under it.

var (
	// ErrNameTooLong reports a name over the 255 bytes Linux allows for one
	// path component: 85 Chinese characters fill it.
	ErrNameTooLong = fmt.Errorf("%w: longer than %d bytes", ErrInvalidName, maxNameBytes)
	// ErrNameNotPortable reports a name Windows or macOS cannot hold: control
	// characters, \ / : * ? " < > |, a trailing space or dot, or a device name
	// such as CON.
	ErrNameNotPortable = fmt.Errorf("%w: not usable on Windows or macOS", ErrInvalidName)
)

// notPortable are the characters Windows forbids in names, besides the
// path separators validName already refuses.
const notPortable = `:*?"<>|`

// reservedNames are Windows device names, refused with or without an
// extension and in any case.
var reservedNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

func init() {
	for digit := '1'; digit <= '9'; digit++ {
		reservedNames["COM"+string(digit)] = true
		reservedNames["LPT"+string(digit)] = true
	}
}

// checkNewName checks a name a user chose for a new or renamed item.
func checkNewName(name string) error {
	if !validName(name) || strings.HasPrefix(name, ".a-nas-") {
		return ErrInvalidName
	}
	if len(name) > maxNameBytes {
		return ErrNameTooLong
	}
	if !utf8.ValidString(name) || strings.ContainsAny(name, notPortable) || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return ErrNameNotPortable
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return ErrNameNotPortable
		}
	}
	stem, _, _ := strings.Cut(name, ".")
	if reservedNames[strings.ToUpper(strings.TrimRight(stem, " "))] {
		return ErrNameNotPortable
	}
	return nil
}

// checkName checks the name an item is to have: a name the user chose must
// follow the rules for new names, while keeping the item's own name only
// needs to be a valid one.
func checkName(name, current string) error {
	if name == current {
		if !validName(name) {
			return ErrInvalidName
		}
		return nil
	}
	return checkNewName(name)
}

// caseTwin returns the item in a folder whose name differs from name only in
// letter case, other than except. SMB clients on Windows and macOS cannot
// tell such names apart, so it counts as taken. The catalog may lag behind
// changes made over SMB until the folder is next listed.
func (s *Service) caseTwin(ctx context.Context, spaceID, parentID, name, except string) (Entry, bool, error) {
	// SQLite folds ASCII only, so the folder's names are compared here.
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, name, kind FROM entries
WHERE space_id = ? AND parent_id = ? AND trashed = 0 AND id <> ? AND length(name) = length(?) AND `+placed,
		spaceID, parentID, except, name)
	if err != nil {
		return Entry{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.ID, &entry.Name, &entry.Kind); err != nil {
			return Entry{}, false, err
		}
		if entry.Name != name && strings.EqualFold(entry.Name, name) {
			entry.SpaceID, entry.ParentID = spaceID, parentID
			return entry, true, rows.Err()
		}
	}
	return Entry{}, false, rows.Err()
}

// refuseCaseTwin returns ErrConflict when a folder already holds name in
// another letter case.
func (s *Service) refuseCaseTwin(ctx context.Context, spaceID, parentID, name, except string) error {
	_, found, err := s.caseTwin(ctx, spaceID, parentID, name, except)
	if err != nil {
		return err
	}
	if found {
		return ErrConflict
	}
	return nil
}

// naming says how a name given to createDirectory or upload is checked.
type naming struct {
	// current is the name the item had; keeping it only needs a valid name.
	current string
	// keep accepts any valid name, for the contents of a folder restored
	// from a snapshot, which go into a folder created for them.
	keep bool
	// twins refuses a name already in the folder in another letter case.
	twins bool
}

var (
	// nameChosen is a name the user typed or uploaded.
	nameChosen = naming{twins: true}
	// nameKept is an existing item's name copied into a new folder.
	nameKept = naming{keep: true}
)

// keeping is a name the user may have kept from an existing item.
func keeping(current string) naming { return naming{current: current, twins: true} }

func (n naming) check(name string) error {
	if n.keep {
		if !validName(name) {
			return ErrInvalidName
		}
		return nil
	}
	return checkName(name, n.current)
}
