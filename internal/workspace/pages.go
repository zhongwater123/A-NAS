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
	err := q.QueryRowContext(ctx, "SELECT id, title, created_by, created_by_name, created_at, latest_version FROM pages WHERE id = ?", id).
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
	var summary PageSummary
	var content []byte
	var createdAt string
	query := `SELECT ` + versionColumns + `, v.content, p.id, p.title, p.created_by, p.created_by_name, p.created_at
FROM pages p JOIN versions v ON v.page_id = p.id WHERE p.id = ? AND v.version = `
	arguments := []any{pageID}
	if version > 0 {
		query += "?"
		arguments = append(arguments, version)
	} else {
		query += "p.latest_version"
	}
	info, err := scanVersion(s.db.QueryRowContext(ctx, query, arguments...).Scan, &content, &summary.ID, &summary.Title,
		&summary.CreatedBy.ID, &summary.CreatedBy.Name, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, PageSummary{}, ErrNotFound
	}
	if err != nil {
		return nil, PageSummary{}, err
	}
	summary.Latest, summary.CreatedAt = info, parseTime(createdAt)
	return content, summary, nil
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
	title := shortTitle(cleanTitle(Title(content)))
	if title == "" {
		title = shortTitle(cleanTitle(strings.TrimSuffix(path.Base(strings.ReplaceAll(fileName, `\`, "/")), path.Ext(fileName))))
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "INSERT INTO pages(id, title, created_by, created_by_name, created_at, latest_version, updated_at) VALUES(?,?,?,?,?,1,?)",
		id, title, actor.AccountID, actor.Name, formatTime(now), formatTime(now)); err != nil {
		return Page{}, err
	}
	if err := s.insertVersion(ctx, tx, id, 1, content, actor, via, note, nil, now); err != nil {
		return Page{}, err
	}
	if err := s.audit(ctx, tx, actor, "workspace.page_created", "page", id, title); err != nil {
		return Page{}, err
	}
	page, err := s.page(ctx, tx, id)
	if err != nil {
		return Page{}, err
	}
	return page, tx.Commit()
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
	return s.commitVersion(ctx, actor, pageID, func(latest VersionInfo, _ []byte) ([]byte, []int, error) {
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
	info, err := s.commitVersion(ctx, actor, pageID, func(latest VersionInfo, source []byte) ([]byte, []int, error) {
		onTopOf = latest.Version
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

// commitVersion reads the latest version, lets change make the next one, and
// stores it. The caller holds s.writes.
func (s *Store) commitVersion(ctx context.Context, actor Actor, pageID string, change func(VersionInfo, []byte) ([]byte, []int, error), via Via, note string) (VersionInfo, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VersionInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var title string
	var source []byte
	latest, err := scanVersion(tx.QueryRowContext(ctx, `SELECT `+versionColumns+`, v.content, p.title
FROM pages p JOIN versions v ON v.page_id = p.id AND v.version = p.latest_version WHERE p.id = ?`, pageID).Scan, &source, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return VersionInfo{}, ErrNotFound
	}
	if err != nil {
		return VersionInfo{}, err
	}
	content, changed, err := change(latest, source)
	if err != nil {
		return VersionInfo{}, err
	}
	next := latest.Version + 1
	now := s.now()
	if err := s.insertVersion(ctx, tx, pageID, next, content, actor, via, note, changed, now); err != nil {
		return VersionInfo{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE pages SET latest_version = ?, updated_at = ? WHERE id = ?", next, formatTime(now), pageID); err != nil {
		return VersionInfo{}, err
	}
	if err := s.audit(ctx, tx, actor, "workspace.page_saved", "page", pageID, fmt.Sprintf("%s v%d", title, next)); err != nil {
		return VersionInfo{}, err
	}
	info, err := scanVersion(tx.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM versions v WHERE v.page_id = ? AND v.version = ?", pageID, next).Scan)
	if err != nil {
		return VersionInfo{}, err
	}
	return info, tx.Commit()
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PageSummary{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, "UPDATE pages SET title = ? WHERE id = ?", title, pageID)
	if err != nil {
		return PageSummary{}, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return PageSummary{}, ErrNotFound
	}
	if err := s.audit(ctx, tx, actor, "workspace.page_renamed", "page", pageID, title); err != nil {
		return PageSummary{}, err
	}
	page, err := s.page(ctx, tx, pageID)
	if err != nil {
		return PageSummary{}, err
	}
	return page.PageSummary, tx.Commit()
}

// DeletePage removes a page and all its versions. Only the account that
// uploaded it and administrators may.
func (s *Store) DeletePage(ctx context.Context, actor Actor, pageID string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var createdBy, title string
	err = tx.QueryRowContext(ctx, "SELECT created_by, title FROM pages WHERE id = ?", pageID).Scan(&createdBy, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if createdBy != actor.AccountID && !actor.Admin {
		return ErrForbidden
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM pages WHERE id = ?", pageID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM transfers WHERE page_id = ?", pageID); err != nil {
		return err
	}
	if err := s.audit(ctx, tx, actor, "workspace.page_deleted", "page", pageID, title); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.live.forgetPage(pageID)
	return nil
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

// shortTitle cuts a title taken from a page or a file name to MaxTitleRunes.
func shortTitle(title string) string {
	if runes := []rune(title); len(runes) > MaxTitleRunes {
		return string(runes[:MaxTitleRunes])
	}
	return title
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
