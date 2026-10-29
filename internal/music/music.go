// Package music is the music library: it indexes the audio files in each
// member's personal space and in the shared space where they already are,
// and keeps every member's favorites, play history, playlists and music
// folders. Files are listed and opened through files.Service, so in
// production every read runs as the signed-in member through the File Broker
// (ADR 0008, ADR 0018) and the library never opens a file its member cannot.
// Formats browsers cannot play are probed and decoded by the shared media
// worker (ffmpegworker); this package only decides what to ask it for.
package music

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/files"
)

var (
	ErrNotFound = errors.New("music resource not found")
	ErrInvalid  = errors.New("music request is invalid")
	// ErrDecoderUnavailable reports that a track needs the media worker and
	// none can be reached.
	ErrDecoderUnavailable = errors.New("music decoder is unavailable")
	// ErrUndecodable reports that FFmpeg cannot read a track.
	ErrUndecodable = errors.New("music file cannot be decoded")
)

// Files is the part of files.Service the library reads through.
type Files interface {
	Tree(ctx context.Context, actor accounts.User, spaceID, directoryID string) ([]files.TreeEntry, error)
	Lookup(ctx context.Context, actor accounts.User, entryID string) (files.Entry, string, error)
	OpenContent(ctx context.Context, actor accounts.User, entryID string) (files.Content, error)
}

// listFolder lists everything below a music folder ("" for the space root)
// with paths relative to the space root, and returns the folder's own path.
// Tree reconciles the space first, so the path follows a renamed folder.
func (s *Service) listFolder(ctx context.Context, actor accounts.User, spaceID, folderID string) (string, []files.TreeEntry, error) {
	entries, err := s.files.Tree(ctx, actor, spaceID, folderID)
	if err != nil || folderID == "" {
		return "", entries, err
	}
	folder, path, err := s.files.Lookup(ctx, actor, folderID)
	if err != nil {
		return "", nil, err
	}
	if folder.SpaceID != spaceID || folder.Kind != files.EntryKindDirectory {
		return "", nil, files.ErrNotFound
	}
	for index := range entries {
		entries[index].Path = path + "/" + entries[index].Path
	}
	return path, entries, nil
}

// Spaces lists the spaces a member can see.
type Spaces interface {
	ListSpaces(ctx context.Context, actor accounts.User) ([]accounts.Space, error)
}

type Options struct {
	Now    func() time.Time
	Logger *slog.Logger
	// Worker reads and decodes the formats browsers cannot play. Without
	// one they are indexed from their names and cannot be played.
	Worker ffmpegworker.Worker
}

type Service struct {
	store   *Store
	covers  coverStore
	files   Files
	spaces  Spaces
	worker  ffmpegworker.Worker
	now     func() time.Time
	logger  *slog.Logger
	version atomic.Int64

	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// work lets one scan read files at a time, so scans of several members
	// do not compete for the disk.
	work  chan struct{}
	mu    sync.Mutex
	scans map[string]*scanJob
}

// New opens the library on store, keeping cover renditions under coverRoot.
func New(store *Store, coverRoot string, fileService Files, spaces Spaces, options Options) *Service {
	base, cancel := context.WithCancel(context.Background())
	service := &Service{
		store: store, covers: coverStore{root: coverRoot}, files: fileService, spaces: spaces, worker: options.Worker,
		now: options.Now, logger: options.Logger,
		base: base, cancel: cancel, work: make(chan struct{}, 1), scans: map[string]*scanJob{},
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	// Clients compare versions to know when to reload tracks; starting from
	// the clock makes a restarted service never repeat an earlier version.
	service.version.Store(service.now().UnixMilli())
	return service
}

// Close stops running scans and waits for them.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

// Track is one audio file in the library. Its ID is the file's stable ID in
// its space, so favorites and playlists survive renames and moves.
type Track struct {
	ID          string   `json:"id"`
	SpaceID     string   `json:"spaceId"`
	Folder      string   `json:"folder"`
	Name        string   `json:"name"`
	SizeBytes   int64    `json:"sizeBytes"`
	Format      string   `json:"format"`
	Codec       string   `json:"codec,omitempty"`
	Title       string   `json:"title"`
	Artist      string   `json:"artist,omitempty"`
	Artists     []string `json:"artists,omitempty"`
	Album       string   `json:"album,omitempty"`
	AlbumArtist string   `json:"albumArtist,omitempty"`
	AlbumID     string   `json:"albumId"`
	Genres      []string `json:"genres,omitempty"`
	Year        int      `json:"year,omitempty"`
	TrackNo     int      `json:"trackNo,omitempty"`
	DiscNo      int      `json:"discNo,omitempty"`
	DurationMs  int64    `json:"durationMs,omitempty"`
	CoverID     string   `json:"coverId,omitempty"`
	Lyrics      bool     `json:"lyrics,omitempty"`
	// Transcode marks a track the media worker decodes while it plays; its
	// stream cannot seek, so players restart it from a start position.
	Transcode bool      `json:"transcode,omitempty"`
	AddedAt   time.Time `json:"addedAt"`
}

// discFolder matches folders that hold one disc of an album, such as "CD2".
var discFolder = regexp.MustCompile(`(?i)^(cd|disc|disk|碟)\s*[0-9]+$`)

// albumDir is the folder an album lives in: a disc folder belongs to its parent.
func albumDir(dir string) string {
	if dir != "" && discFolder.MatchString(path.Base(dir)) {
		if parent := path.Dir(dir); parent != "." {
			return parent
		}
		return ""
	}
	return dir
}

// albumID groups tracks into albums: by album artist when the tags name one,
// otherwise by album title within one folder, so a compilation whose tracks
// credit different artists stays one album.
func albumID(value row) string {
	if strings.TrimSpace(value.Album) == "" {
		return "album:unknown"
	}
	key := "dir\x00" + value.SpaceID + "\x00" + albumDir(value.Dir) + "\x00" + strings.ToLower(value.Album)
	if value.AlbumArtist != "" {
		key = "artist\x00" + strings.ToLower(value.AlbumArtist) + "\x00" + strings.ToLower(value.Album)
	}
	sum := sha256.Sum256([]byte(key))
	return "album:" + hex.EncodeToString(sum[:12])
}

func (value row) track() Track {
	cover := value.EmbeddedCover
	if cover == "" {
		cover = value.FolderCover
	}
	return Track{
		ID: value.ID, SpaceID: value.SpaceID, Folder: value.Dir, Name: value.Name, SizeBytes: value.SizeBytes,
		Format: value.Format, Codec: value.Codec, Title: value.Title, Artist: value.Artist,
		Artists: splitArtists(value.Artist), Album: value.Album, AlbumArtist: value.AlbumArtist,
		AlbumID: albumID(value), Genres: splitGenres(value.Genre), Year: value.Year, TrackNo: value.TrackNo,
		DiscNo: value.DiscNo, DurationMs: value.DurationMs, CoverID: cover,
		Lyrics: value.EmbeddedLyrics || value.LyricsFile != "", Transcode: !playsInBrowser(value.Format, value.Codec), AddedAt: value.AddedAt,
	}
}

// ContentType is the media type a track is streamed with.
func (t Track) ContentType() string {
	if t.Transcode {
		return "audio/flac"
	}
	return contentTypes[t.Format]
}

// Library is every track a member can play. Version changes whenever a scan
// changes any track.
type Library struct {
	Version int64   `json:"version"`
	Items   []Track `json:"items"`
}

// Tracks returns the member's library.
func (s *Service) Tracks(ctx context.Context, actor accounts.User) (Library, error) {
	version := s.version.Load()
	scopes, err := s.scopes(ctx, actor)
	if err != nil {
		return Library{}, err
	}
	rows, err := s.store.rowsIn(ctx, scopes)
	if err != nil {
		return Library{}, err
	}
	library := Library{Version: version, Items: make([]Track, 0, len(rows))}
	for _, value := range rows {
		library.Items = append(library.Items, value.track())
	}
	sort.Slice(library.Items, func(i, j int) bool {
		left, right := library.Items[i], library.Items[j]
		if left.SpaceID != right.SpaceID {
			return left.SpaceID < right.SpaceID
		}
		if left.Folder != right.Folder {
			return left.Folder < right.Folder
		}
		return left.Name < right.Name
	})
	return library, nil
}

// visibleTrack returns a track of the member's library.
func (s *Service) visibleTrack(ctx context.Context, actor accounts.User, id string) (row, error) {
	value, err := s.store.rowByID(ctx, id)
	if err != nil {
		return row{}, err
	}
	scopes, err := s.scopes(ctx, actor)
	if err != nil {
		return row{}, err
	}
	if !inScopes(scopes, value.SpaceID, value.Dir) {
		return row{}, ErrNotFound
	}
	return value, nil
}

// Track returns a track of the member's library.
func (s *Service) Track(ctx context.Context, actor accounts.User, id string) (Track, error) {
	value, err := s.visibleTrack(ctx, actor, id)
	if err != nil {
		return Track{}, err
	}
	return value.track(), nil
}

// Decode streams a track that browsers cannot play as FLAC from start
// seconds, decoded by the media worker from the file opened as the member.
func (s *Service) Decode(ctx context.Context, actor accounts.User, id string, start float64) (io.ReadCloser, error) {
	value, err := s.visibleTrack(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if playsInBrowser(value.Format, value.Codec) || start < 0 || value.DurationMs > 0 && start*1000 > float64(value.DurationMs) {
		return nil, ErrInvalid
	}
	if s.worker == nil {
		return nil, ErrDecoderUnavailable
	}
	content, err := s.files.OpenContent(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	// The worker receives its own descriptor; this one closes once the
	// request is sent.
	defer func() { _ = content.Reader.Close() }()
	stream, err := s.worker.Audio(ctx, content.Reader, playbackRequest(value.SampleRate, value.BitDepth, start))
	return stream, decoderError(err)
}

func decoderError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ffmpegworker.ErrUnavailable):
		return fmt.Errorf("%w: %w", ErrDecoderUnavailable, err)
	case errors.Is(err, ffmpegworker.ErrUnreadable), errors.Is(err, ffmpegworker.ErrInvalid):
		return fmt.Errorf("%w: %w", ErrUndecodable, err)
	}
	return err
}

// Stream is an open track ready to be served with Range support.
type Stream struct {
	files.Content
	Track Track
}

// OpenTrack opens a track of the member's library for playback, as the member.
func (s *Service) OpenTrack(ctx context.Context, actor accounts.User, id string) (Stream, error) {
	value, err := s.visibleTrack(ctx, actor, id)
	if err != nil {
		return Stream{}, err
	}
	content, err := s.files.OpenContent(ctx, actor, id)
	if err != nil {
		return Stream{}, err
	}
	return Stream{Content: content, Track: value.track()}, nil
}

// maxLyricsBytes bounds the lyrics files the library reads.
const maxLyricsBytes = 1 << 20

// Lyrics returns a track's lyrics: a .lrc file with the same name beside it,
// or else lyrics embedded in its tags. Synchronised lyrics keep their LRC
// time tags for the client.
func (s *Service) Lyrics(ctx context.Context, actor accounts.User, id string) (string, error) {
	value, err := s.visibleTrack(ctx, actor, id)
	if err != nil {
		return "", err
	}
	if value.LyricsFile != "" {
		content, err := s.files.OpenContent(ctx, actor, value.LyricsFile)
		if err == nil {
			defer func() { _ = content.Reader.Close() }()
			raw, err := io.ReadAll(io.LimitReader(content.Reader, maxLyricsBytes))
			if err != nil {
				return "", err
			}
			return decodeText(raw), nil
		}
		if !errors.Is(err, files.ErrNotFound) {
			return "", err
		}
	}
	if !value.EmbeddedLyrics {
		return "", ErrNotFound
	}
	content, err := s.files.OpenContent(ctx, actor, id)
	if err != nil {
		return "", err
	}
	defer func() { _ = content.Reader.Close() }()
	lyrics := readLyrics(content.Reader)
	if lyrics == "" {
		return "", ErrNotFound
	}
	return lyrics, nil
}

// CoverPath returns the rendition of a cover that belongs to a track of the
// member's library.
func (s *Service) CoverPath(ctx context.Context, actor accounts.User, id, size string) (string, error) {
	if !validCoverID(id) {
		return "", ErrNotFound
	}
	rows, err := s.store.queryRows(ctx, "WHERE embedded_cover = ? OR folder_cover = ?", id, id)
	if err != nil {
		return "", err
	}
	scopes, err := s.scopes(ctx, actor)
	if err != nil {
		return "", err
	}
	for _, value := range rows {
		if inScopes(scopes, value.SpaceID, value.Dir) {
			file, err := s.covers.Path(id, size)
			if err != nil {
				return "", ErrNotFound
			}
			return file, nil
		}
	}
	return "", ErrNotFound
}

// scope is the part of a space one music folder covers; Path "" is the
// whole space.
type scope struct {
	SpaceID string
	Path    string
}

func inScopes(scopes []scope, spaceID, dir string) bool {
	for _, scope := range scopes {
		if scope.SpaceID == spaceID && (scope.Path == "" || dir == scope.Path || strings.HasPrefix(dir, scope.Path+"/")) {
			return true
		}
	}
	return false
}

// Source is a music folder: a folder, or a whole space, the library indexes
// for its member.
type Source struct {
	SpaceID   string `json:"spaceId"`
	SpaceKind string `json:"spaceKind"`
	FolderID  string `json:"folderId,omitempty"`
	Path      string `json:"path"`
}

// Sources lists the member's music folders. Until the member chooses some,
// the library covers their whole personal space and the shared space.
type Sources struct {
	Custom bool     `json:"custom"`
	Items  []Source `json:"items"`
}

// ownSpaces are the spaces a member's library may cover. Spaces seen through
// Administrative Viewing Mode are not the member's music.
func (s *Service) ownSpaces(ctx context.Context, actor accounts.User) (map[string]accounts.Space, []accounts.Space, error) {
	spaces, err := s.spaces.ListSpaces(ctx, actor)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]accounts.Space{}
	var own []accounts.Space
	for _, space := range spaces {
		if space.Viewing != nil {
			continue
		}
		byID[space.ID] = space
		own = append(own, space)
	}
	return byID, own, nil
}

func (s *Service) Sources(ctx context.Context, actor accounts.User) (Sources, error) {
	byID, own, err := s.ownSpaces(ctx, actor)
	if err != nil {
		return Sources{}, err
	}
	var custom bool
	err = s.store.db.QueryRowContext(ctx, "SELECT custom_sources FROM settings WHERE user_id = ?", actor.ID).Scan(&custom)
	if err != nil && !errors.Is(err, errNoRows) {
		return Sources{}, err
	}
	result := Sources{Custom: custom, Items: []Source{}}
	if !custom {
		for _, space := range own {
			result.Items = append(result.Items, Source{SpaceID: space.ID, SpaceKind: string(space.Kind)})
		}
		return result, nil
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT space_id, folder_id, path FROM sources WHERE user_id = ? ORDER BY position", actor.ID)
	if err != nil {
		return Sources{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var source Source
		if err := rows.Scan(&source.SpaceID, &source.FolderID, &source.Path); err != nil {
			return Sources{}, err
		}
		space, ok := byID[source.SpaceID]
		if !ok {
			continue
		}
		source.SpaceKind = string(space.Kind)
		result.Items = append(result.Items, source)
	}
	return result, rows.Err()
}

// scopes are the parts of spaces the member's library covers, without
// folders nested in another of them.
func (s *Service) scopes(ctx context.Context, actor accounts.User) ([]scope, error) {
	sources, err := s.Sources(ctx, actor)
	if err != nil {
		return nil, err
	}
	var scopes []scope
	for _, source := range sources.Items {
		scopes = append(scopes, scope{SpaceID: source.SpaceID, Path: source.Path})
	}
	return normalizeScopes(scopes), nil
}

func normalizeScopes(scopes []scope) []scope {
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].SpaceID != scopes[j].SpaceID {
			return scopes[i].SpaceID < scopes[j].SpaceID
		}
		return scopes[i].Path < scopes[j].Path
	})
	var result []scope
	for _, candidate := range scopes {
		if inScopes(result, candidate.SpaceID, candidate.Path) {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

// SourceInput names a music folder to keep; FolderID "" is the whole space.
type SourceInput struct {
	SpaceID  string `json:"spaceId"`
	FolderID string `json:"folderId,omitempty"`
}

const maxSources = 32

// SetSources replaces the member's music folders. useDefault returns to the
// whole personal space and the shared space.
func (s *Service) SetSources(ctx context.Context, actor accounts.User, inputs []SourceInput, useDefault bool) (Sources, error) {
	if len(inputs) > maxSources {
		return Sources{}, ErrInvalid
	}
	byID, _, err := s.ownSpaces(ctx, actor)
	if err != nil {
		return Sources{}, err
	}
	type resolved struct {
		input SourceInput
		path  string
	}
	var chosen []resolved
	if !useDefault {
		for _, input := range inputs {
			if _, ok := byID[input.SpaceID]; !ok {
				return Sources{}, ErrNotFound
			}
			folder := ""
			if input.FolderID != "" {
				folder, _, err = s.listFolder(ctx, actor, input.SpaceID, input.FolderID)
				if err != nil {
					return Sources{}, err
				}
			}
			chosen = append(chosen, resolved{input: input, path: folder})
		}
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Sources{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sources WHERE user_id = ?", actor.ID); err != nil {
		return Sources{}, err
	}
	for position, item := range chosen {
		if _, err := tx.ExecContext(ctx, "INSERT INTO sources(user_id, position, space_id, folder_id, path) VALUES(?,?,?,?,?)",
			actor.ID, position, item.input.SpaceID, item.input.FolderID, item.path); err != nil {
			return Sources{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(user_id, custom_sources) VALUES(?, ?)
ON CONFLICT(user_id) DO UPDATE SET custom_sources = excluded.custom_sources`, actor.ID, !useDefault); err != nil {
		return Sources{}, err
	}
	if err := tx.Commit(); err != nil {
		return Sources{}, err
	}
	s.version.Add(1)
	return s.Sources(ctx, actor)
}

// Favorite is a track the member marked with a heart.
type Favorite struct {
	TrackID string    `json:"trackId"`
	AddedAt time.Time `json:"addedAt"`
}

// Favorites lists the member's favorites, newest first. They may name tracks
// that are not in the library at the moment, for example while the data
// volume is offline; clients show only tracks they have.
func (s *Service) Favorites(ctx context.Context, actor accounts.User) ([]Favorite, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT track_id, added_at FROM favorites WHERE user_id = ? ORDER BY added_at DESC", actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Favorite{}
	for rows.Next() {
		var item Favorite
		var added string
		if err := rows.Scan(&item.TrackID, &added); err != nil {
			return nil, err
		}
		item.AddedAt, _ = time.Parse(time.RFC3339Nano, added)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) SetFavorite(ctx context.Context, actor accounts.User, trackID string, favorite bool) error {
	if !favorite {
		_, err := s.store.db.ExecContext(ctx, "DELETE FROM favorites WHERE user_id = ? AND track_id = ?", actor.ID, trackID)
		return err
	}
	if _, err := s.visibleTrack(ctx, actor, trackID); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, "INSERT OR IGNORE INTO favorites(user_id, track_id, added_at) VALUES(?,?,?)",
		actor.ID, trackID, formatTime(s.now()))
	return err
}

// Play is a track in the member's play history.
type Play struct {
	TrackID  string    `json:"trackId"`
	PlayedAt time.Time `json:"playedAt"`
	Plays    int       `json:"plays"`
}

// recentLimit is how many tracks the play history keeps.
const recentLimit = 300

// RecordPlay notes that the member played a track.
func (s *Service) RecordPlay(ctx context.Context, actor accounts.User, trackID string) error {
	if _, err := s.visibleTrack(ctx, actor, trackID); err != nil {
		return err
	}
	now := formatTime(s.now())
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO plays(user_id, track_id, played_at, play_count) VALUES(?,?,?,1)
ON CONFLICT(user_id, track_id) DO UPDATE SET played_at = excluded.played_at, play_count = play_count + 1`,
		actor.ID, trackID, now); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, `DELETE FROM plays WHERE user_id = ? AND track_id NOT IN (
SELECT track_id FROM plays WHERE user_id = ? ORDER BY played_at DESC LIMIT ?)`, actor.ID, actor.ID, recentLimit)
	return err
}

// Recent lists the member's play history, most recent first.
func (s *Service) Recent(ctx context.Context, actor accounts.User) ([]Play, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT track_id, played_at, play_count FROM plays WHERE user_id = ? ORDER BY played_at DESC LIMIT ?", actor.ID, recentLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Play{}
	for rows.Next() {
		var item Play
		var played string
		if err := rows.Scan(&item.TrackID, &played, &item.Plays); err != nil {
			return nil, err
		}
		item.PlayedAt, _ = time.Parse(time.RFC3339Nano, played)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) ClearRecent(ctx context.Context, actor accounts.User) error {
	_, err := s.store.db.ExecContext(ctx, "DELETE FROM plays WHERE user_id = ?", actor.ID)
	return err
}

// Playlist is a member's ordered list of tracks. A track may appear more than
// once. Like favorites, it may name tracks the library does not hold now.
type Playlist struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TrackIDs  []string  `json:"trackIds"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const (
	maxPlaylistName   = 80
	maxPlaylistTracks = 5000
)

func (s *Service) Playlists(ctx context.Context, actor accounts.User) ([]Playlist, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT id, name, created_at, updated_at FROM playlists WHERE user_id = ? ORDER BY created_at", actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Playlist{}
	for rows.Next() {
		var item Playlist
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Name, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		items = append(items, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for index := range items {
		if items[index].TrackIDs, err = s.playlistTracks(ctx, items[index].ID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Service) playlistTracks(ctx context.Context, id string) ([]string, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT track_id FROM playlist_tracks WHERE playlist_id = ? ORDER BY position", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var trackID string
		if err := rows.Scan(&trackID); err != nil {
			return nil, err
		}
		ids = append(ids, trackID)
	}
	return ids, rows.Err()
}

func (s *Service) playlist(ctx context.Context, actor accounts.User, id string) (Playlist, error) {
	var item Playlist
	var created, updated string
	err := s.store.db.QueryRowContext(ctx, "SELECT id, name, created_at, updated_at FROM playlists WHERE id = ? AND user_id = ?", id, actor.ID).
		Scan(&item.ID, &item.Name, &created, &updated)
	if errors.Is(err, errNoRows) {
		return Playlist{}, ErrNotFound
	}
	if err != nil {
		return Playlist{}, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	item.TrackIDs, err = s.playlistTracks(ctx, id)
	return item, err
}

func playlistName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxPlaylistName || strings.ContainsAny(name, "\x00\r\n") {
		return "", ErrInvalid
	}
	return name, nil
}

func validTrackIDs(ids []string) bool {
	if len(ids) > maxPlaylistTracks {
		return false
	}
	for _, id := range ids {
		if id == "" || len(id) > 128 {
			return false
		}
	}
	return true
}

func (s *Service) CreatePlaylist(ctx context.Context, actor accounts.User, name string, trackIDs []string) (Playlist, error) {
	name, err := playlistName(name)
	if err != nil || !validTrackIDs(trackIDs) {
		return Playlist{}, ErrInvalid
	}
	id := s.randomID("playlist")
	now := formatTime(s.now())
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Playlist{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO playlists(id, user_id, name, created_at, updated_at) VALUES(?,?,?,?,?)", id, actor.ID, name, now, now); err != nil {
		return Playlist{}, err
	}
	if err := insertPlaylistTracks(ctx, tx, id, 0, trackIDs); err != nil {
		return Playlist{}, err
	}
	if err := tx.Commit(); err != nil {
		return Playlist{}, err
	}
	return s.playlist(ctx, actor, id)
}

// PlaylistChange renames a playlist and/or replaces its tracks.
type PlaylistChange struct {
	Name     *string   `json:"name,omitempty"`
	TrackIDs *[]string `json:"trackIds,omitempty"`
}

func (s *Service) UpdatePlaylist(ctx context.Context, actor accounts.User, id string, change PlaylistChange) (Playlist, error) {
	if _, err := s.playlist(ctx, actor, id); err != nil {
		return Playlist{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Playlist{}, err
	}
	defer tx.Rollback()
	if change.Name != nil {
		name, err := playlistName(*change.Name)
		if err != nil {
			return Playlist{}, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE playlists SET name = ? WHERE id = ?", name, id); err != nil {
			return Playlist{}, err
		}
	}
	if change.TrackIDs != nil {
		if !validTrackIDs(*change.TrackIDs) {
			return Playlist{}, ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM playlist_tracks WHERE playlist_id = ?", id); err != nil {
			return Playlist{}, err
		}
		if err := insertPlaylistTracks(ctx, tx, id, 0, *change.TrackIDs); err != nil {
			return Playlist{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE playlists SET updated_at = ? WHERE id = ?", formatTime(s.now()), id); err != nil {
		return Playlist{}, err
	}
	if err := tx.Commit(); err != nil {
		return Playlist{}, err
	}
	return s.playlist(ctx, actor, id)
}

// AddToPlaylist appends tracks of the member's library to a playlist.
func (s *Service) AddToPlaylist(ctx context.Context, actor accounts.User, id string, trackIDs []string) (Playlist, error) {
	current, err := s.playlist(ctx, actor, id)
	if err != nil {
		return Playlist{}, err
	}
	if len(trackIDs) == 0 || !validTrackIDs(append(current.TrackIDs, trackIDs...)) {
		return Playlist{}, ErrInvalid
	}
	for _, trackID := range trackIDs {
		if _, err := s.visibleTrack(ctx, actor, trackID); err != nil {
			return Playlist{}, err
		}
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Playlist{}, err
	}
	defer tx.Rollback()
	if err := insertPlaylistTracks(ctx, tx, id, len(current.TrackIDs), trackIDs); err != nil {
		return Playlist{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE playlists SET updated_at = ? WHERE id = ?", formatTime(s.now()), id); err != nil {
		return Playlist{}, err
	}
	if err := tx.Commit(); err != nil {
		return Playlist{}, err
	}
	return s.playlist(ctx, actor, id)
}

func insertPlaylistTracks(ctx context.Context, tx execer, id string, start int, trackIDs []string) error {
	for offset, trackID := range trackIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO playlist_tracks(playlist_id, position, track_id) VALUES(?,?,?)", id, start+offset, trackID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) DeletePlaylist(ctx context.Context, actor accounts.User, id string) error {
	result, err := s.store.db.ExecContext(ctx, "DELETE FROM playlists WHERE id = ? AND user_id = ?", id, actor.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) randomID(prefix string) string { return prefix + ":" + rand.Text() }
