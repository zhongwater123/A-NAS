package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// rename gives one file or folder a new name inside its folder.
type rename struct{ id, parent, from, to string }

func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\\x00") && !strings.HasPrefix(name, ".") && len(name) <= 255
}

// videoRenames plans a video file's new name, with the subtitles, NFO
// documents and artwork beside it that carry its name, so they stay
// attached. siblings are the files of its folder; a planned name that is
// already taken is a conflict.
func videoRenames(entry files.Entry, name string, siblings []files.Entry) ([]rename, error) {
	renames := []rename{{entry.ID, entry.ParentID, entry.Name, name}}
	oldBase, newBase := baseName(entry.Name), baseName(name)
	taken := make(map[string]bool, len(siblings))
	for _, sibling := range siblings {
		taken[sibling.Name] = true
		if sibling.ID == entry.ID || sibling.Kind != files.EntryKindFile || isVideo(sibling.Name) {
			continue
		}
		// Heat (1995).chs.srt, Heat (1995).nfo, Heat (1995)-poster.jpg.
		rest, ok := strings.CutPrefix(sibling.Name, oldBase)
		if ok && rest != "" && (rest[0] == '.' || rest[0] == '-') && path.Ext(rest) != "" {
			renames = append(renames, rename{sibling.ID, entry.ParentID, sibling.Name, newBase + rest})
		}
	}
	for _, change := range renames {
		if taken[change.to] {
			return nil, fmt.Errorf("%w: %s", files.ErrConflict, change.to)
		}
	}
	return renames, nil
}

// applyRenames renames in order and, if one fails, puts back what it
// renamed, so a video and its files keep matching names.
func (s *Service) applyRenames(ctx context.Context, actor accounts.User, renames []rename) error {
	for index, change := range renames {
		if _, err := s.files.Move(ctx, actor, change.id, "", change.parent, change.to); err != nil {
			undo := context.WithoutCancel(ctx)
			for back := index - 1; back >= 0; back-- {
				_, _ = s.files.Move(undo, actor, renames[back].id, "", renames[back].parent, renames[back].from)
			}
			return err
		}
	}
	return nil
}

// RenameVideo renames a video's file as the actor, together with the
// subtitles, NFO documents and artwork beside it that carry its name. Files
// keep their IDs across a rename, so progress, favorites and collections
// stay; the library is rescanned to read the title from the new name.
func (s *Service) RenameVideo(ctx context.Context, actor accounts.User, videoID, name string) (VideoDetail, error) {
	video, library, err := s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return VideoDetail{}, err
	}
	// A scan must not publish an old snapshot between file moves and the
	// catalog update. Reload after waiting, because another rename may have run.
	unlock := s.lockRename(ctx, actor, library.ID)
	defer unlock()
	video, library, err = s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return VideoDetail{}, err
	}
	name = strings.TrimSpace(name)
	if !validName(name) {
		return VideoDetail{}, fmt.Errorf("%w: 文件名无效", ErrInvalid)
	}
	if !isVideo(name) {
		return VideoDetail{}, fmt.Errorf("%w: 文件名需要保留视频扩展名", ErrInvalid)
	}
	if name == video.FileName {
		return s.Video(ctx, actor, videoID)
	}
	entry, _, err := s.files.Lookup(ctx, actor, video.EntryID)
	if err != nil {
		return VideoDetail{}, err
	}
	siblings, err := s.files.List(ctx, actor, library.SpaceID, entry.ParentID)
	if err != nil {
		return VideoDetail{}, err
	}
	renames, err := videoRenames(entry, name, siblings)
	if err != nil {
		return VideoDetail{}, err
	}
	if err := s.applyRenames(ctx, actor, renames); err != nil {
		return VideoDetail{}, err
	}
	// The detail page shows the new name at once; the title follows the scan.
	if _, err := s.store.db.ExecContext(ctx, "UPDATE videos SET file_name = ? WHERE entry_id = ?", name, video.EntryID); err != nil {
		return VideoDetail{}, err
	}
	return s.Video(ctx, actor, videoID)
}

// showFolder finds the folder a show has to itself: the one its episodes
// lie in, or the parent of their season folders, when no other video of
// the library lies below it. A show whose episodes lie in the library
// folder itself, or share a folder with other titles, has none.
func (s *Service) showFolder(ctx context.Context, actor accounts.User, show *showRow) (files.Entry, bool, error) {
	if len(show.episodes) == 0 {
		return files.Entry{}, false, nil
	}
	showPath := func(video *videoRow) (string, bool) {
		parts := strings.Split(video.Folder, "/")
		if video.Folder == "" {
			return "", false
		}
		if _, season := seasonOfFolder(parts[len(parts)-1]); season {
			return strings.Join(parts[:len(parts)-1], "/"), true
		}
		return video.Folder, false
	}
	first := show.episodes[0]
	folder, inSeason := showPath(first)
	if folder == "" {
		return files.Entry{}, false, nil
	}
	for _, video := range show.episodes[1:] {
		if other, _ := showPath(video); other != folder || video.RootEntry != first.RootEntry {
			return files.Entry{}, false, nil
		}
	}
	var others int
	if err := s.store.db.QueryRowContext(ctx, `
SELECT count(*) FROM videos WHERE library_id = ?1 AND root_entry = ?2
AND (folder = ?3 OR substr(folder, 1, length(?3) + 1) = ?3 || '/') AND (show_id IS NULL OR show_id <> ?4)`,
		show.LibraryID, first.RootEntry, folder, show.ID).Scan(&others); err != nil {
		return files.Entry{}, false, err
	}
	if others > 0 {
		return files.Entry{}, false, nil
	}
	entry, _, err := s.files.Lookup(ctx, actor, first.EntryID)
	if err != nil {
		return files.Entry{}, false, err
	}
	steps := 1
	if inSeason {
		steps = 2
	}
	for ; steps > 0; steps-- {
		if entry, _, err = s.files.Lookup(ctx, actor, entry.ParentID); err != nil {
			return files.Entry{}, false, err
		}
	}
	if entry.Kind != files.EntryKindDirectory || entry.Name != path.Base(folder) {
		return files.Entry{}, false, nil
	}
	return entry, true, nil
}

// titleIn finds a title in a file name whatever separates its words.
func titleIn(title string) *regexp.Regexp {
	words := strings.Fields(title)
	if len(words) == 0 {
		return nil
	}
	for index, word := range words {
		words[index] = regexp.QuoteMeta(word)
	}
	return regexp.MustCompile(`(?i)` + strings.Join(words, `[\s._-]+`))
}

// RenameShow renames a show as the actor: its own folder, if it has one,
// and every episode file whose name carries the show's title, with the
// files named after each episode. The show keeps its ID, so favorites and
// collections stay, and the library is rescanned under the new name.
func (s *Service) RenameShow(ctx context.Context, actor accounts.User, showID, name string) (ShowDetail, error) {
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return ShowDetail{}, err
	}
	show, ok := c.shows[showID]
	if !ok || len(show.episodes) == 0 {
		return ShowDetail{}, ErrNotFound
	}
	library, err := s.library(ctx, actor, show.LibraryID)
	if err != nil {
		return ShowDetail{}, err
	}
	unlock := s.lockRename(ctx, actor, library.ID)
	defer unlock()
	c, err = s.loadCatalog(ctx, actor)
	if err != nil {
		return ShowDetail{}, err
	}
	show, ok = c.shows[showID]
	if !ok || len(show.episodes) == 0 {
		return ShowDetail{}, ErrNotFound
	}
	name = strings.TrimSpace(name)
	if !validName(name) {
		return ShowDetail{}, fmt.Errorf("%w: 名称无效", ErrInvalid)
	}
	folder, own, err := s.showFolder(ctx, actor, show)
	if err != nil {
		return ShowDetail{}, err
	}
	newTitle := cleanTitle(name)
	if own {
		// A folder name may carry the year: 漫长的季节 (2023).
		newTitle, _ = folderTitle(name)
	}
	if newTitle == "" {
		return ShowDetail{}, fmt.Errorf("%w: 名称无效", ErrInvalid)
	}
	// The scan finds shows by their normalized title. Another show holding
	// it is a conflict whatever the files say, so it is checked before any
	// file is looked up: right after a rename the episodes still name the
	// old files until the scan catches up.
	key := normalizeTitle(newTitle)
	if key == "" {
		key = newTitle
	}
	var holder string
	err = s.store.db.QueryRowContext(ctx, "SELECT id FROM shows WHERE library_id = ? AND key = ? AND id <> ?", show.LibraryID, key, showID).Scan(&holder)
	switch {
	case err == nil:
		return ShowDetail{}, fmt.Errorf("%w: 媒体库里已有同名的剧", files.ErrConflict)
	case !errors.Is(err, sql.ErrNoRows):
		return ShowDetail{}, err
	}

	var renames []rename
	renamed := map[string]string{}
	listed := map[string][]files.Entry{}
	if pattern := titleIn(show.Title); pattern != nil {
		for _, video := range show.episodes {
			base := baseName(video.FileName)
			found := pattern.FindStringIndex(base)
			if found == nil {
				continue
			}
			newName := base[:found[0]] + newTitle + base[found[1]:] + path.Ext(video.FileName)
			if newName == video.FileName {
				continue
			}
			entry, _, err := s.files.Lookup(ctx, actor, video.EntryID)
			if err != nil {
				return ShowDetail{}, err
			}
			siblings, ok := listed[entry.ParentID]
			if !ok {
				if siblings, err = s.files.List(ctx, actor, library.SpaceID, entry.ParentID); err != nil {
					return ShowDetail{}, err
				}
				listed[entry.ParentID] = siblings
			}
			plan, err := videoRenames(entry, newName, siblings)
			if err != nil {
				return ShowDetail{}, err
			}
			renames = append(renames, plan...)
			renamed[video.EntryID] = newName
		}
	}
	// The folder goes last, after the files inside it.
	if own && folder.Name != name {
		renames = append(renames, rename{folder.ID, folder.ParentID, folder.Name, name})
	}
	if len(renames) == 0 {
		if !own {
			return ShowDetail{}, fmt.Errorf("%w: 这部剧没有自己的文件夹，文件名里也没有剧名，请分别重命名各集的片源", ErrInvalid)
		}
		return s.Show(ctx, actor, showID)
	}
	// Moving this show to the new title before the scan lets the scan keep
	// its ID.
	if err := s.applyRenames(ctx, actor, renames); err != nil {
		return ShowDetail{}, err
	}
	if _, err := s.store.db.ExecContext(ctx, "UPDATE shows SET key = ?, title = ? WHERE id = ?", key, newTitle, showID); err != nil {
		return ShowDetail{}, err
	}
	for entryID, fileName := range renamed {
		if _, err := s.store.db.ExecContext(ctx, "UPDATE videos SET file_name = ? WHERE entry_id = ?", fileName, entryID); err != nil {
			return ShowDetail{}, err
		}
	}
	return s.Show(ctx, actor, showID)
}

// namedByNFO reports whether an NFO document gives a video's or show's
// title, which then does not follow its file or folder name.
func (s *Service) namedByNFO(ctx context.Context, table, id string) bool {
	var document string
	if err := s.store.db.QueryRowContext(ctx, "SELECT nfo FROM "+table+" WHERE id = ?", id).Scan(&document); err != nil || document == "" {
		return false
	}
	var fields nfoFields
	return json.Unmarshal([]byte(document), &fields) == nil && fields.Title != ""
}

// lockRename shares the scan's exclusion boundary for the entire file/catalog
// mutation and response. Always request a fresh scan after unlocking: a scan
// requested while we held the lock may have coalesced into the "again" flag,
// even when this rename failed validation or rolled its file changes back.
func (s *Service) lockRename(ctx context.Context, actor accounts.User, libraryID string) func() {
	lock := s.scanLock(libraryID)
	lock.Lock()
	return func() {
		lock.Unlock()
		s.ScanInBackground(ctx, actor, libraryID)
	}
}
