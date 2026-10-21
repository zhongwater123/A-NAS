package files

import (
	"context"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// ShareSource is an explicitly selected ordinary file or directory. Version is
// only a change detector; it is not a substitute for atomic file capture.
type ShareSource struct {
	Entry
	Path    string `json:"path"`
	Version string `json:"version"`
}

func (s *Service) shareRoot(ctx context.Context, actor accounts.User, spaceID string) (string, error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return "", err
	}
	if space.Viewing != nil || (space.Kind == accounts.SpaceKindPrivate && space.OwnerUserID != actor.ID) {
		return "", ErrForbidden
	}
	return root, nil
}

// ShareSelection refuses partial trees and deduplicates selected descendants.
// It deliberately does not grant redistribution through administrator viewing.
func (s *Service) ShareSelection(ctx context.Context, actor accounts.User, ids []string) ([]ShareSource, error) {
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, ErrInvalidSelection
	}
	type selected struct {
		entry          Entry
		relative, root string
	}
	chosen := []selected{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		e, rel, err := s.Lookup(ctx, actor, id)
		if err != nil {
			return nil, err
		}
		root, err := s.shareRoot(ctx, actor, e.SpaceID)
		if err != nil {
			return nil, err
		}
		chosen = append(chosen, selected{e, rel, root})
	}
	sort.Slice(chosen, func(i, j int) bool {
		if chosen[i].entry.SpaceID != chosen[j].entry.SpaceID {
			return chosen[i].entry.SpaceID < chosen[j].entry.SpaceID
		}
		return chosen[i].relative < chosen[j].relative
	})
	var result []ShareSource
	roots := []selected{}
	names := map[string]bool{}
	for _, item := range chosen {
		covered := false
		for _, prior := range roots {
			if prior.entry.SpaceID == item.entry.SpaceID && prior.entry.Kind == EntryKindDirectory && strings.HasPrefix(item.relative, prior.relative+"/") {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		roots = append(roots, item)
		name := item.entry.Name
		for n := 1; names[strings.ToLower(name)]; n++ {
			name = numberedName(item.entry.Name, n)
		}
		names[strings.ToLower(name)] = true
		full := filepath.Join(item.root, filepath.FromSlash(item.relative))
		info, err := s.fs.Lstat(ctx, full)
		if err != nil {
			return nil, err
		}
		appendSource := func(e Entry, p string, info FileInfo) error {
			if (e.Kind == EntryKindDirectory) != (info.Kind == KindDirectory) {
				return ErrInvalidSelection
			}
			if info.Kind != KindFile && info.Kind != KindDirectory {
				return ErrUnsupportedType
			}
			e.SizeBytes = info.Size
			e.ModifiedAt = info.ModTime
			result = append(result, ShareSource{Entry: e, Path: p, Version: strconv.FormatUint(info.Inode, 10) + ":" + strconv.FormatInt(info.ModTime.UnixNano(), 10) + ":" + strconv.FormatInt(info.Size, 10)})
			if len(result) > 10000 {
				return ErrInvalidSelection
			}
			return nil
		}
		if err := appendSource(item.entry, name, info); err != nil {
			return nil, err
		}
		if info.Kind != KindDirectory {
			continue
		}
		skip := archiveSkip
		skip.Strict = true
		skip.Limit = max(1, 10000-len(result))
		// The folder's contents are catalogued from the scan, which a change
		// to the space during it would make stale.
		err = s.scanAlone(ctx, item.entry.SpaceID, func() error {
			children, err := s.fs.Scan(ctx, full, skip)
			if err != nil {
				return err
			}
			parents := map[string]string{"": item.entry.ID}
			for _, child := range children {
				if child.Info.Kind != KindFile && child.Info.Kind != KindDirectory {
					return ErrUnsupportedType
				}
				parent := path.Dir(child.Path)
				if parent == "." {
					parent = ""
				}
				parentID, ok := parents[parent]
				if !ok {
					return ErrInvalidSelection
				}
				entry, err := s.registerInfo(ctx, s.store.db, item.root, item.entry.SpaceID, parentID, path.Join(item.relative, child.Path), child.Info)
				if err != nil {
					return err
				}
				if err := appendSource(entry, path.Join(name, child.Path), child.Info); err != nil {
					return err
				}
				if child.Info.Kind == KindDirectory {
					parents[child.Path] = entry.ID
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// OpenShareContent opens only content that may be redistributed by this actor.
func (s *Service) OpenShareContent(ctx context.Context, actor accounts.User, id string) (Content, error) {
	entry, _, err := s.Lookup(ctx, actor, id)
	if err != nil {
		return Content{}, err
	}
	if _, err := s.shareRoot(ctx, actor, entry.SpaceID); err != nil {
		return Content{}, err
	}
	return s.OpenContent(ctx, actor, id)
}
