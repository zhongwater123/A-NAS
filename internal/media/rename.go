package media

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// RenameVideo renames a video's file as the actor, together with the
// subtitles, NFO documents and artwork beside it that carry its name, so
// they stay attached to it. Files keep their IDs across a rename, so
// progress, favorites and collections stay; the library is rescanned to
// read the title from the new name.
func (s *Service) RenameVideo(ctx context.Context, actor accounts.User, videoID, name string) (VideoDetail, error) {
	video, library, err := s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return VideoDetail{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, ".") || len(name) > 255 {
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

	type rename struct{ id, from, to string }
	renames := []rename{{video.EntryID, entry.Name, name}}
	oldBase, newBase := baseName(entry.Name), baseName(name)
	taken := make(map[string]bool, len(siblings))
	for _, sibling := range siblings {
		taken[sibling.Name] = true
		if sibling.ID == video.EntryID || sibling.Kind != files.EntryKindFile || isVideo(sibling.Name) {
			continue
		}
		// Heat (1995).chs.srt, Heat (1995).nfo, Heat (1995)-poster.jpg.
		rest, ok := strings.CutPrefix(sibling.Name, oldBase)
		if ok && rest != "" && (rest[0] == '.' || rest[0] == '-') && path.Ext(rest) != "" {
			renames = append(renames, rename{sibling.ID, sibling.Name, newBase + rest})
		}
	}
	for _, change := range renames {
		if taken[change.to] {
			return VideoDetail{}, fmt.Errorf("%w: %s", files.ErrConflict, change.to)
		}
	}
	var done []rename
	for _, change := range renames {
		if _, err := s.files.Move(ctx, actor, change.id, "", entry.ParentID, change.to); err != nil {
			// Put back what was renamed, so the video and its files keep
			// matching names.
			undo := context.WithoutCancel(ctx)
			for index := len(done) - 1; index >= 0; index-- {
				_, _ = s.files.Move(undo, actor, done[index].id, "", entry.ParentID, done[index].from)
			}
			return VideoDetail{}, err
		}
		done = append(done, change)
	}
	// The detail page shows the new name at once; the title follows the scan.
	if _, err := s.store.db.ExecContext(ctx, "UPDATE videos SET file_name = ? WHERE entry_id = ?", name, video.EntryID); err != nil {
		return VideoDetail{}, err
	}
	s.ScanInBackground(ctx, actor, library.ID)
	return s.Video(ctx, actor, videoID)
}
