package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Person is an account as it was named when it acted.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// VersionInfo describes one saved version of a page.
type VersionInfo struct {
	Version int    `json:"version"`
	Size    int64  `json:"size"`
	SavedBy Person `json:"savedBy"`
	Via     Via    `json:"via"`
	Note    string `json:"note"`
	// Changed holds the offsets of the elements an agent's edit changed in
	// this version, for the open page to point at.
	Changed []int     `json:"changed"`
	SavedAt time.Time `json:"savedAt"`
}

// PageSummary is a page as the list shows it.
type PageSummary struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	CreatedBy Person      `json:"createdBy"`
	CreatedAt time.Time   `json:"createdAt"`
	Latest    VersionInfo `json:"latest"`
}

// Page is a page with all its versions, newest first.
type Page struct {
	PageSummary
	Versions []VersionInfo `json:"versions"`
}

const versionColumns = "v.version, v.size, v.saved_by, v.saved_by_name, v.via_kind, v.via_name, v.note, v.changed, v.saved_at"

func scanVersion(scan func(...any) error, extra ...any) (VersionInfo, error) {
	var info VersionInfo
	var kind, changed, savedAt string
	if err := scan(append([]any{&info.Version, &info.Size, &info.SavedBy.ID, &info.SavedBy.Name, &kind, &info.Via.Name, &info.Note, &changed, &savedAt}, extra...)...); err != nil {
		return VersionInfo{}, err
	}
	info.Via.Kind = ViaKind(kind)
	info.SavedAt = parseTime(savedAt)
	info.Changed = []int{}
	_ = json.Unmarshal([]byte(changed), &info.Changed)
	return info, nil
}

// ListPages lists the pages, the most recently saved first.
func (s *Store) ListPages(ctx context.Context) ([]PageSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+versionColumns+`, p.id, p.title, p.created_by, p.created_by_name, p.created_at
FROM pages p JOIN versions v ON v.page_id = p.id AND v.version = p.latest_version
WHERE p.deleted_at = ''
ORDER BY p.updated_at DESC, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pages := []PageSummary{}
	for rows.Next() {
		var page PageSummary
		var createdAt string
		latest, err := scanVersion(rows.Scan, &page.ID, &page.Title, &page.CreatedBy.ID, &page.CreatedBy.Name, &createdAt)
		if err != nil {
			return nil, err
		}
		page.Latest, page.CreatedAt = latest, parseTime(createdAt)
		pages = append(pages, page)
	}
	return pages, rows.Err()
}

// Page returns a page with its versions.
func (s *Store) Page(ctx context.Context, id string) (Page, error) {
	return s.page(ctx, s.db, id)
}

func (s *Store) page(ctx context.Context, q queryer, id string) (Page, error) {
	var page Page
	var createdAt string
	var latest int
	err := q.QueryRowContext(ctx, "SELECT id, title, created_by, created_by_name, created_at, latest_version FROM pages WHERE id = ? AND deleted_at = ''", id).
		Scan(&page.ID, &page.Title, &page.CreatedBy.ID, &page.CreatedBy.Name, &createdAt, &latest)
	if errors.Is(err, sql.ErrNoRows) {
		return Page{}, ErrNotFound
	}
	if err != nil {
		return Page{}, err
	}
	page.CreatedAt = parseTime(createdAt)
	rows, err := q.QueryContext(ctx, "SELECT "+versionColumns+" FROM versions v WHERE v.page_id = ? ORDER BY v.version DESC", id)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		info, err := scanVersion(rows.Scan)
		if err != nil {
			return Page{}, err
		}
		page.Versions = append(page.Versions, info)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Versions) == 0 || page.Versions[0].Version != latest {
		return Page{}, fmt.Errorf("page %s lost its latest version", id)
	}
	page.Latest = page.Versions[0]
	return page, nil
}

// Content returns a version's source and what it was; version 0 is the
// latest.
func (s *Store) Content(ctx context.Context, pageID string, version int) ([]byte, PageSummary, error) {
	var content []byte
	summary, _, err := s.describe(ctx, pageID, version, &content)
	return content, summary, err
}

// Version returns what a version was without its source, and the page's
// latest version; version 0 is the latest.
func (s *Store) Version(ctx context.Context, pageID string, version int) (PageSummary, int, error) {
	return s.describe(ctx, pageID, version, nil)
}

// describe reads a version, and its source into content unless that is nil.
func (s *Store) describe(ctx context.Context, pageID string, version int, content *[]byte) (PageSummary, int, error) {
	var summary PageSummary
	var createdAt string
	var latest int
	columns := versionColumns + ", p.id, p.title, p.created_by, p.created_by_name, p.created_at, p.latest_version"
	targets := []any{&summary.ID, &summary.Title, &summary.CreatedBy.ID, &summary.CreatedBy.Name, &createdAt, &latest}
	if content != nil {
		columns += ", v.content"
		targets = append(targets, content)
	}
	query := "SELECT " + columns + " FROM pages p JOIN versions v ON v.page_id = p.id WHERE p.id = ? AND p.deleted_at = '' AND v.version = "
	arguments := []any{pageID}
	if version > 0 {
		query += "?"
		arguments = append(arguments, version)
	} else {
		query += "p.latest_version"
	}
	info, err := scanVersion(s.db.QueryRowContext(ctx, query, arguments...).Scan, targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return PageSummary{}, 0, ErrNotFound
	}
	if err != nil {
		return PageSummary{}, 0, err
	}
	summary.Latest, summary.CreatedAt = info, parseTime(createdAt)
	return summary, latest, nil
}

// CreatePage stores a new page as its version 1. Its title is the page's
// <title>, else the file's name.
func (s *Store) CreatePage(ctx context.Context, actor Actor, fileName string, content []byte, via Via, note string) (Page, error) {
	if err := checkContent(content); err != nil {
		return Page{}, err
	}
	note, err := cleanNote(note)
	if err != nil {
		return Page{}, err
	}
	title := Cut(cleanTitle(Title(content)), MaxTitleRunes)
	if title == "" {
		title = Cut(cleanTitle(strings.TrimSuffix(path.Base(strings.ReplaceAll(fileName, `\`, "/")), path.Ext(fileName))), MaxTitleRunes)
	}
	if title == "" || title == "." || title == "/" {
		title = "未命名页面"
	}
	if err := s.admit(len(content)); err != nil {
		return Page{}, err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	id := newID("page")
	now := s.now()
	var page Page
	err = sqlitedb.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO pages(id, title, created_by, created_by_name, created_at, latest_version, updated_at) VALUES(?,?,?,?,?,1,?)",
			id, title, actor.AccountID, actor.Name, formatTime(now), formatTime(now)); err != nil {
			return err
		}
		if err := s.insertVersion(ctx, tx, id, 1, content, actor, via, note, nil, now); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, actor, "workspace.page_created", "page", id, title); err != nil {
			return err
		}
		page, err = s.page(ctx, tx, id)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	return page, nil
}

// SaveVersion stores content as the page's next version. base is the
// version the content replaces; when it is no longer the latest, nothing is
// saved and a *PageChangedError names the latest.
func (s *Store) SaveVersion(ctx context.Context, actor Actor, pageID string, base int, content []byte, via Via, note string) (VersionInfo, error) {
	if err := checkContent(content); err != nil {
		return VersionInfo{}, err
	}
	note, err := cleanNote(note)
	if err != nil {
		return VersionInfo{}, err
	}
	if err := s.admit(len(content)); err != nil {
		return VersionInfo{}, err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	return s.commitVersion(ctx, actor, pageID, func(latest VersionInfo, _ func() ([]byte, error)) ([]byte, []int, error) {
		if base != latest.Version {
			return nil, nil, &PageChangedError{Latest: latest}
		}
		return content, nil, nil
	}, via, note)
}

// Edit replaces OldText, which must occur exactly once in the latest version,
// with NewText.
type Edit struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// EditConflictError refuses an edit whose old text is not, or not once, in
// the latest version. Nothing of the call was saved.
type EditConflictError struct {
	// Index is the 1-based edit that failed; Occurrences how often its old
	// text occurs in the latest version.
	Index       int
	Occurrences int
	Folded      bool
	Latest      int
}

func (e *EditConflictError) Error() string {
	switch {
	case e.Folded:
		return fmt.Sprintf("edit %d copies folded inline data (%q…); download the whole page with workspace_checkout to change it", e.Index, FoldedMarker)
	case e.Occurrences == 0:
		return fmt.Sprintf("edit %d: its old_text is not in the latest version v%d; someone may have changed that part: read it again", e.Index, e.Latest)
	default:
		return fmt.Sprintf("edit %d: its old_text occurs %d times in v%d; copy more of the text around it", e.Index, e.Occurrences, e.Latest)
	}
}

// EditResult is what an edit saved.
type EditResult struct {
	Version VersionInfo
	// OnTopOf is the version the edits were applied to; it is later than the
	// base the caller read when others saved in between.
	OnTopOf int
	// Lines are the 1-based line ranges of the new text in the new version.
	Lines [][2]int
}

// EditPage applies edits in order to the latest version and saves the result
// as the next one. Others' versions saved since base are kept: an edit only
// fails where its old text is gone or not unique.
func (s *Store) EditPage(ctx context.Context, actor Actor, pageID string, base int, edits []Edit, via Via, note string) (EditResult, error) {
	if len(edits) == 0 || len(edits) > MaxEdits {
		return EditResult{}, fmt.Errorf("%w: give 1–%d edits", ErrInvalid, MaxEdits)
	}
	note, err := cleanNote(note)
	if err != nil {
		return EditResult{}, err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	var onTopOf int
	var lines [][2]int
	info, err := s.commitVersion(ctx, actor, pageID, func(latest VersionInfo, read func() ([]byte, error)) ([]byte, []int, error) {
		onTopOf = latest.Version
		source, err := read()
		if err != nil {
			return nil, nil, err
		}
		updated, positions, err := ApplyEdits(source, edits, latest.Version)
		if err != nil {
			return nil, nil, err
		}
		if err := checkContent(updated); err != nil {
			return nil, nil, err
		}
		if err := s.admit(len(updated)); err != nil {
			return nil, nil, err
		}
		for i, position := range positions {
			lines = append(lines, [2]int{LineAt(updated, position), LineAt(updated, max(position, position+len(edits[i].NewText)-1))})
		}
		return updated, Deepest(updated, positions), nil
	}, via, note)
	if err != nil {
		return EditResult{}, err
	}
	return EditResult{Version: info, OnTopOf: onTopOf, Lines: lines}, nil
}

// ApplyEdits applies edits in order to source, which is version latest, and
// returns the result with where each new text starts in it.
func ApplyEdits(source []byte, edits []Edit, latest int) ([]byte, []int, error) {
	text := string(source)
	positions := make([]int, 0, len(edits))
	for i, edit := range edits {
		if edit.OldText == "" {
			return nil, nil, fmt.Errorf("%w: edit %d has no old_text", ErrInvalid, i+1)
		}
		if strings.Contains(edit.OldText, FoldedMarker) {
			return nil, nil, &EditConflictError{Index: i + 1, Folded: true, Latest: latest}
		}
		if count := strings.Count(text, edit.OldText); count != 1 {
			return nil, nil, &EditConflictError{Index: i + 1, Occurrences: count, Latest: latest}
		}
		at := strings.Index(text, edit.OldText)
		text = text[:at] + edit.NewText + text[at+len(edit.OldText):]
		for j := range positions {
			if positions[j] > at {
				positions[j] += len(edit.NewText) - len(edit.OldText)
			}
		}
		positions = append(positions, at)
	}
	if text == string(source) {
		return nil, nil, fmt.Errorf("%w: the edits change nothing", ErrInvalid)
	}
	return []byte(text), positions, nil
}

// commitVersion lets change make the next version from the latest, reading
// the latest's source only when change asks, and stores it. The caller holds
// s.writes, so the latest stays the latest meanwhile and change works outside
// the transaction, which holds the store's only connection.
func (s *Store) commitVersion(ctx context.Context, actor Actor, pageID string, change func(VersionInfo, func() ([]byte, error)) ([]byte, []int, error), via Via, note string) (VersionInfo, error) {
	summary, _, err := s.Version(ctx, pageID, 0)
	if err != nil {
		return VersionInfo{}, err
	}
	latest := summary.Latest
	content, changed, err := change(latest, func() ([]byte, error) {
		source, _, err := s.Content(ctx, pageID, latest.Version)
		return source, err
	})
	if err != nil {
		return VersionInfo{}, err
	}
	if changed == nil {
		changed = []int{}
	}
	next := latest.Version + 1
	now := s.now()
	err = sqlitedb.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.insertVersion(ctx, tx, pageID, next, content, actor, via, note, changed, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE pages SET latest_version = ?, updated_at = ? WHERE id = ?", next, formatTime(now), pageID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "workspace.page_saved", "page", pageID, fmt.Sprintf("%s v%d", summary.Title, next))
	})
	if err != nil {
		return VersionInfo{}, err
	}
	s.live.saved(via.TokenID, pageID)
	// As scanVersion would read it back.
	return VersionInfo{
		Version: next, Size: int64(len(content)), SavedBy: Person{ID: actor.AccountID, Name: actor.Name},
		Via: Via{Kind: via.Kind, Name: via.Name}, Note: note, Changed: changed, SavedAt: parseTime(formatTime(now)),
	}, nil
}

func (s *Store) insertVersion(ctx context.Context, q queryer, pageID string, version int, content []byte, actor Actor, via Via, note string, changed []int, at time.Time) error {
	if !validVia(via.Kind) {
		return fmt.Errorf("%w: unknown way of saving %q", ErrInvalid, via.Kind)
	}
	if changed == nil {
		changed = []int{}
	}
	encoded, _ := json.Marshal(changed)
	digest := sha256.Sum256(content)
	_, err := q.ExecContext(ctx, `INSERT INTO versions(page_id, version, content, size, sha256, saved_by, saved_by_name, via_kind, via_name, token_id, note, changed, saved_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, pageID, version, content, len(content), hex.EncodeToString(digest[:]), actor.AccountID, actor.Name,
		string(via.Kind), via.Name, via.TokenID, note, string(encoded), formatTime(at))
	return err
}

// RenamePage changes a page's title.
func (s *Store) RenamePage(ctx context.Context, actor Actor, pageID, title string) (PageSummary, error) {
	title = cleanTitle(title)
	if title == "" || utf8.RuneCountInString(title) > MaxTitleRunes {
		return PageSummary{}, fmt.Errorf("%w: a title has 1–%d characters", ErrInvalid, MaxTitleRunes)
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	var page Page
	err := sqlitedb.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE pages SET title = ? WHERE id = ? AND deleted_at = ''", title, pageID)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed == 0 {
			return ErrNotFound
		}
		if err := s.audit(ctx, tx, actor, "workspace.page_renamed", "page", pageID, title); err != nil {
			return err
		}
		page, err = s.page(ctx, tx, pageID)
		return err
	})
	if err != nil {
		return PageSummary{}, err
	}
	return page.PageSummary, nil
}

// DeletePage moves a page with all its versions to the trash, where it can
// be brought back for TrashDays (RestorePage). Only the account that
// uploaded it and administrators may.
func (s *Store) DeletePage(ctx context.Context, actor Actor, pageID string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	err := sqlitedb.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		title, err := s.mayManage(ctx, tx, actor, pageID, false)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE pages SET deleted_at = ?, deleted_by = ?, deleted_by_name = ? WHERE id = ?",
			formatTime(s.now()), actor.AccountID, actor.Name, pageID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM transfers WHERE page_id = ?", pageID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "workspace.page_deleted", "page", pageID, title)
	})
	if err != nil {
		return err
	}
	s.live.forgetPage(pageID)
	return nil
}

// mayManage returns the title of a page in the list, or in the trash when
// trashed is true, if actor uploaded it or is an administrator.
func (s *Store) mayManage(ctx context.Context, q queryer, actor Actor, pageID string, trashed bool) (string, error) {
	var createdBy, title, deletedAt string
	err := q.QueryRowContext(ctx, "SELECT created_by, title, deleted_at FROM pages WHERE id = ?", pageID).Scan(&createdBy, &title, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (deletedAt != "") != trashed {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if createdBy != actor.AccountID && !actor.Admin {
		return "", ErrForbidden
	}
	return title, nil
}

func checkContent(content []byte) error {
	if len(content) > MaxPageBytes {
		return ErrTooLarge
	}
	if len(content) == 0 {
		return fmt.Errorf("%w: the page is empty", ErrInvalid)
	}
	if !utf8.Valid(content) {
		return fmt.Errorf("%w: save the page as UTF-8", ErrInvalid)
	}
	return nil
}

// cleanTitle folds white space and turns control characters into spaces.
func cleanTitle(title string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)), " ")
}

// Cut keeps value's first runes characters, never splitting one.
func Cut(value string, runes int) string {
	if r := []rune(value); len(r) > runes {
		return string(r[:runes])
	}
	return value
}

func cleanNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > MaxNoteRunes {
		return "", fmt.Errorf("%w: a note has at most %d characters", ErrInvalid, MaxNoteRunes)
	}
	return note, nil
}

func validVia(kind ViaKind) bool {
	switch kind {
	case ViaWeb, ViaClaudeCode, ViaCodex, ViaOther:
		return true
	}
	return false
}
