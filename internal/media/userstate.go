package media

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

const (
	// watchedShare marks a video watched once this much of it was played.
	watchedShare = 0.9
	// resumeFloor: progress before this many seconds is not worth resuming.
	resumeFloor       = 30
	maxCollectionName = 40
	setPrefix         = "set:"
)

// SaveProgress records where the user is in a video. Reaching 90% marks it
// watched and clears the position, so it leaves "continue watching".
func (s *Service) SaveProgress(ctx context.Context, actor accounts.User, videoID string, position, duration float64) (Progress, error) {
	video, _, err := s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return Progress{}, err
	}
	if math.IsNaN(position) || math.IsInf(position, 0) || position < 0 || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 {
		return Progress{}, ErrInvalid
	}
	if video.Duration > 0 {
		duration = video.Duration
	}
	watched := duration > 0 && position >= duration*watchedShare
	if watched || position < resumeFloor {
		position = 0
	}
	now := s.now()
	// Finishing keeps an earlier watched mark; starting again does not undo it
	// until the user passes the floor.
	_, err = s.store.db.ExecContext(ctx, `
INSERT INTO progress(user_id, video_id, position, duration, watched, played_at, in_history) VALUES(?,?,?,?,?,?,1)
ON CONFLICT(user_id, video_id) DO UPDATE SET position = excluded.position, duration = excluded.duration,
watched = CASE WHEN excluded.watched THEN 1 WHEN excluded.position > 0 THEN 0 ELSE progress.watched END,
played_at = excluded.played_at, in_history = 1`,
		actor.ID, videoID, position, duration, watched, formatTime(now))
	if err != nil {
		return Progress{}, err
	}
	var progress Progress
	err = s.store.db.QueryRowContext(ctx, "SELECT position, duration, watched FROM progress WHERE user_id = ? AND video_id = ?", actor.ID, videoID).
		Scan(&progress.Position, &progress.Duration, &progress.Watched)
	progress.PlayedAt = &now
	return progress, err
}

// SetWatched marks a video, or every episode of a show, watched or not.
func (s *Service) SetWatched(ctx context.Context, actor accounts.User, id string, watched bool) error {
	var ids []string
	if strings.HasPrefix(id, "show:") {
		c, err := s.loadCatalog(ctx, actor)
		if err != nil {
			return err
		}
		show, ok := c.shows[id]
		if !ok {
			return ErrNotFound
		}
		for _, episode := range show.episodes {
			ids = append(ids, episode.ID)
		}
	} else {
		if _, _, err := s.visibleVideo(ctx, actor, id); err != nil {
			return err
		}
		ids = []string{id}
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, videoID := range ids {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO progress(user_id, video_id, position, watched) VALUES(?,?,0,?)
ON CONFLICT(user_id, video_id) DO UPDATE SET position = 0, watched = excluded.watched`, actor.ID, videoID, watched); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RemoveFromHistory hides a video from the user's history; its progress and
// watched mark stay.
func (s *Service) RemoveFromHistory(ctx context.Context, actor accounts.User, videoID string) error {
	result, err := s.store.db.ExecContext(ctx, "UPDATE progress SET in_history = 0 WHERE user_id = ? AND video_id = ?", actor.ID, videoID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) ClearHistory(ctx context.Context, actor accounts.User) error {
	_, err := s.store.db.ExecContext(ctx, "UPDATE progress SET in_history = 0 WHERE user_id = ?", actor.ID)
	return err
}

// visibleTarget checks that a video or show ID is visible to the actor.
func (s *Service) visibleTarget(ctx context.Context, actor accounts.User, id string) error {
	var libraryID string
	var err error
	switch {
	case strings.HasPrefix(id, "video:"):
		err = s.store.db.QueryRowContext(ctx, "SELECT library_id FROM videos WHERE id = ?", id).Scan(&libraryID)
	case strings.HasPrefix(id, "show:"):
		err = s.store.db.QueryRowContext(ctx, "SELECT library_id FROM shows WHERE id = ?", id).Scan(&libraryID)
	default:
		return ErrNotFound
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.library(ctx, actor, libraryID)
	return err
}

func (s *Service) SetFavorite(ctx context.Context, actor accounts.User, id string, favorite bool) error {
	if err := s.visibleTarget(ctx, actor, id); err != nil {
		return err
	}
	var err error
	if favorite {
		_, err = s.store.db.ExecContext(ctx, "INSERT INTO favorites(user_id, target_id, created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING",
			actor.ID, id, formatTime(s.now()))
	} else {
		_, err = s.store.db.ExecContext(ctx, "DELETE FROM favorites WHERE user_id = ? AND target_id = ?", actor.ID, id)
	}
	return err
}

func validCollectionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxCollectionName {
		return "", fmt.Errorf("%w: name must be 1-%d characters", ErrInvalid, maxCollectionName)
	}
	return name, nil
}

// Collections lists the user's own collections, then the automatic ones
// that NFO files declare for visible films.
func (s *Service) Collections(ctx context.Context, actor accounts.User) ([]Collection, error) {
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return nil, err
	}
	collections := []Collection{}
	rows, err := s.store.db.QueryContext(ctx, "SELECT id, name, updated_at FROM collections WHERE owner_user_id = ? ORDER BY updated_at DESC", actor.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var collection Collection
		var updated string
		if err := rows.Scan(&collection.ID, &collection.Name, &updated); err != nil {
			_ = rows.Close()
			return nil, err
		}
		collection.UpdatedAt = parseTime(updated)
		collections = append(collections, collection)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range collections {
		members, err := s.members(ctx, c, collections[i].ID)
		if err != nil {
			return nil, err
		}
		collections[i].Count, collections[i].Covers = len(members), covers(members)
	}
	sets := map[string][]Title{}
	for _, video := range c.order {
		if video.Kind == TypeMovie && video.Collection != "" {
			sets[video.Collection] = append(sets[video.Collection], c.videoTitle(video))
		}
	}
	var names []string
	for name, members := range sets {
		if len(members) > 1 {
			names = append(names, name)
		}
	}
	sorter := newSorter()
	sort.Slice(names, func(i, j int) bool { return sorter.collator.CompareString(names[i], names[j]) < 0 })
	for _, name := range names {
		members := sets[name]
		sortByYear(members)
		collections = append(collections, Collection{ID: setID(name), Name: name, Automatic: true, Count: len(members), Covers: covers(members)})
	}
	return collections, nil
}

func setID(name string) string { return setPrefix + base64.RawURLEncoding.EncodeToString([]byte(name)) }

func sortByYear(titles []Title) {
	sort.SliceStable(titles, func(i, j int) bool { return titles[i].Year < titles[j].Year })
}

func covers(titles []Title) []string {
	ids := []string{}
	for _, title := range titles {
		if title.Artwork.Poster || title.Artwork.Thumb {
			ids = append(ids, title.ID)
		}
		if len(ids) == 4 {
			break
		}
	}
	return ids
}

// members lists a user collection's visible titles in the order they were
// added.
func (s *Service) members(ctx context.Context, c *catalog, collectionID string) ([]Title, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT target_id FROM collection_members WHERE collection_id = ? ORDER BY added_at, target_id", collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	titles := []Title{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if title, ok := c.titleOf(id); ok {
			titles = append(titles, title)
		}
	}
	return titles, rows.Err()
}

func (s *Service) ownCollection(ctx context.Context, actor accounts.User, id string) (Collection, error) {
	var collection Collection
	var owner, updated string
	err := s.store.db.QueryRowContext(ctx, "SELECT id, name, owner_user_id, updated_at FROM collections WHERE id = ?", id).Scan(&collection.ID, &collection.Name, &owner, &updated)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != actor.ID) {
		return Collection{}, ErrNotFound
	}
	collection.UpdatedAt = parseTime(updated)
	return collection, err
}

func (s *Service) CollectionDetail(ctx context.Context, actor accounts.User, id string) (CollectionDetail, error) {
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return CollectionDetail{}, err
	}
	if name, ok := strings.CutPrefix(id, setPrefix); ok {
		decoded, err := base64.RawURLEncoding.DecodeString(name)
		if err != nil {
			return CollectionDetail{}, ErrNotFound
		}
		detail := CollectionDetail{Collection: Collection{ID: id, Name: string(decoded), Automatic: true}, Items: []Title{}}
		for _, video := range c.order {
			if video.Kind == TypeMovie && video.Collection == string(decoded) {
				detail.Items = append(detail.Items, c.videoTitle(video))
			}
		}
		if len(detail.Items) == 0 {
			return CollectionDetail{}, ErrNotFound
		}
		sortByYear(detail.Items)
		detail.Count, detail.Covers = len(detail.Items), covers(detail.Items)
		return detail, nil
	}
	collection, err := s.ownCollection(ctx, actor, id)
	if err != nil {
		return CollectionDetail{}, err
	}
	members, err := s.members(ctx, c, id)
	if err != nil {
		return CollectionDetail{}, err
	}
	collection.Count, collection.Covers = len(members), covers(members)
	return CollectionDetail{Collection: collection, Items: members}, nil
}

func (s *Service) CreateCollection(ctx context.Context, actor accounts.User, name string, targets []string) (Collection, error) {
	name, err := validCollectionName(name)
	if err != nil {
		return Collection{}, err
	}
	for _, target := range targets {
		if err := s.visibleTarget(ctx, actor, target); err != nil {
			return Collection{}, err
		}
	}
	now := formatTime(s.now())
	collection := Collection{ID: s.randomID("collection"), Name: name, UpdatedAt: parseTime(now), Covers: []string{}}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Collection{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO collections(id, owner_user_id, name, created_at, updated_at) VALUES(?,?,?,?,?)",
		collection.ID, actor.ID, name, now, now); err != nil {
		return Collection{}, err
	}
	for _, target := range targets {
		if _, err := tx.ExecContext(ctx, "INSERT INTO collection_members(collection_id, target_id, added_at) VALUES(?,?,?) ON CONFLICT DO NOTHING",
			collection.ID, target, now); err != nil {
			return Collection{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Collection{}, err
	}
	collection.Count = len(targets)
	return collection, nil
}

func (s *Service) RenameCollection(ctx context.Context, actor accounts.User, id, name string) error {
	name, err := validCollectionName(name)
	if err != nil {
		return err
	}
	if _, err := s.ownCollection(ctx, actor, id); err != nil {
		return err
	}
	_, err = s.store.db.ExecContext(ctx, "UPDATE collections SET name = ?, updated_at = ? WHERE id = ?", name, formatTime(s.now()), id)
	return err
}

func (s *Service) DeleteCollection(ctx context.Context, actor accounts.User, id string) error {
	if _, err := s.ownCollection(ctx, actor, id); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, "DELETE FROM collections WHERE id = ?", id)
	return err
}

func (s *Service) AddToCollection(ctx context.Context, actor accounts.User, id string, targets []string) error {
	if _, err := s.ownCollection(ctx, actor, id); err != nil {
		return err
	}
	if len(targets) == 0 {
		return ErrInvalid
	}
	for _, target := range targets {
		if err := s.visibleTarget(ctx, actor, target); err != nil {
			return err
		}
	}
	now := formatTime(s.now())
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, target := range targets {
		if _, err := tx.ExecContext(ctx, "INSERT INTO collection_members(collection_id, target_id, added_at) VALUES(?,?,?) ON CONFLICT DO NOTHING", id, target, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE collections SET updated_at = ? WHERE id = ?", now, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) RemoveFromCollection(ctx context.Context, actor accounts.User, id, target string) error {
	if _, err := s.ownCollection(ctx, actor, id); err != nil {
		return err
	}
	result, err := s.store.db.ExecContext(ctx, "DELETE FROM collection_members WHERE collection_id = ? AND target_id = ?", id, target)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	_, err = s.store.db.ExecContext(ctx, "UPDATE collections SET updated_at = ? WHERE id = ?", formatTime(s.now()), id)
	return err
}

// collectionRefs lists the user's collections that hold a title.
func (s *Service) collectionRefs(ctx context.Context, actor accounts.User, target string) ([]CollectionRef, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT c.id, c.name FROM collections c JOIN collection_members m ON m.collection_id = c.id
WHERE c.owner_user_id = ? AND m.target_id = ? ORDER BY c.name`, actor.ID, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []CollectionRef{}
	for rows.Next() {
		var ref CollectionRef
		if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
