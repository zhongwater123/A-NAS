package media

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// staleScan is how old a scan may be before opening the media center scans
// the library again.
const staleScan = 10 * time.Minute

// nfoFields is what a scan keeps from an NFO file, so unchanged files are
// not read again.
type nfoFields struct {
	Root          string   `json:"root"`
	Title         string   `json:"title,omitempty"`
	OriginalTitle string   `json:"originalTitle,omitempty"`
	Year          int      `json:"year,omitempty"`
	Plot          string   `json:"plot,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	Rating        float64  `json:"rating,omitempty"`
	Set           string   `json:"set,omitempty"`
	Season        int      `json:"season,omitempty"`
	Episode       int      `json:"episode,omitempty"`
}

type externalSubtitle struct {
	EntryID  string `json:"entryId"`
	Format   string `json:"format"`
	Language string `json:"language,omitempty"`
	Label    string `json:"label"`
}

// scannedVideo is one video file as the scan understood it.
type scannedVideo struct {
	entry         files.TreeEntry
	rootEntry     string
	folder        string
	kind          TitleType
	title         string
	originalTitle string
	episodeTitle  string
	year          int
	season        int
	episode       int
	plot          string
	genres        []string
	rating        float64
	collection    string
	poster        string
	backdrop      string
	thumb         string
	nfoEntry      string
	nfoModified   string
	nfo           string
	subtitles     []externalSubtitle
	showKey       string
}

type scannedShow struct {
	key         string
	title       string
	year        int
	poster      string
	backdrop    string
	nfoEntry    string
	nfoModified string
	nfo         nfoFields
	nfoJSON     string
}

// ScanInBackground scans a library as actor without blocking the request.
// The request's session token stays in the context.
func (s *Service) ScanInBackground(ctx context.Context, actor accounts.User, libraryID string) {
	scanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
	stop := context.AfterFunc(s.closing, cancel)
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		defer stop()
		defer cancel()
		library, err := s.library(scanCtx, actor, libraryID)
		if err != nil {
			return
		}
		if err := s.scan(scanCtx, actor, library, false); err != nil && !errors.Is(err, files.ErrVolumeUnavailable) {
			s.logger.WarnContext(scanCtx, "media library scan failed", "library_id", libraryID, "error", err)
		}
	}()
}

// Rescan scans a library the actor manages and reads the videos FFmpeg
// could not read before once more.
func (s *Service) Rescan(ctx context.Context, actor accounts.User, libraryID string) error {
	library, err := s.library(ctx, actor, libraryID)
	if err != nil {
		return err
	}
	if !library.manageable(actor) {
		return ErrForbidden
	}
	if _, err := s.store.db.ExecContext(ctx, `
UPDATE videos SET probe_state = CASE WHEN probe_state = 'failed' THEN 'pending' ELSE probe_state END,
frame_state = CASE WHEN frame_state = 'failed' THEN 'pending' ELSE frame_state END
WHERE library_id = ? AND (probe_state = 'failed' OR frame_state = 'failed')`, libraryID); err != nil {
		return err
	}
	s.ScanInBackground(ctx, actor, libraryID)
	return nil
}

// RefreshStale scans the actor's visible libraries whose last scan is older
// than staleScan, as the actor; opening the media center calls it.
func (s *Service) RefreshStale(ctx context.Context, actor accounts.User) {
	libraries, err := s.visibleLibraries(ctx, actor)
	if err != nil {
		return
	}
	for _, library := range libraries {
		if library.ScannedAt == "" || s.now().Sub(parseTime(library.ScannedAt)) > staleScan {
			s.ScanInBackground(ctx, actor, library.ID)
		}
	}
}

// Scan scans a library now as actor and returns when it is done, after any
// scan already running.
func (s *Service) Scan(ctx context.Context, actor accounts.User, libraryID string) error {
	library, err := s.library(ctx, actor, libraryID)
	if err != nil {
		return err
	}
	return s.scan(ctx, actor, library, true)
}

func (s *Service) scanLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.scanLocks[id]
	if !ok {
		lock = &sync.Mutex{}
		s.scanLocks[id] = lock
	}
	return lock
}

func (s *Service) setScanning(id string, scanning bool) {
	s.mu.Lock()
	if scanning {
		s.scanning[id] = true
	} else {
		delete(s.scanning, id)
	}
	s.mu.Unlock()
}

// scan reconciles a library's catalog with its folders, reading as actor.
// Background scans skip a library that is already being scanned.
func (s *Service) scan(ctx context.Context, actor accounts.User, library libraryRow, wait bool) error {
	lock := s.scanLock(library.ID)
	if wait {
		lock.Lock()
	} else if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	s.setScanning(library.ID, true)
	defer s.setScanning(library.ID, false)
	err := s.scanLocked(ctx, actor, library)
	message := ""
	if err != nil {
		message = scanErrorMessage(err)
	}
	if _, updateErr := s.store.db.ExecContext(context.WithoutCancel(ctx), "UPDATE libraries SET scanned_at = ?, scan_error = ? WHERE id = ?",
		formatTime(s.now()), message, library.ID); updateErr != nil && err == nil {
		err = updateErr
	}
	s.wake()
	return err
}

func scanErrorMessage(err error) string {
	switch {
	case errors.Is(err, files.ErrVolumeUnavailable):
		return "数据卷暂时不可用"
	case errors.Is(err, files.ErrForbidden):
		return "没有读取这些文件夹的权限"
	default:
		return "扫描失败，稍后会自动重试"
	}
}

type folderRow struct {
	entryID string
	missing bool
}

func (s *Service) scanLocked(ctx context.Context, actor accounts.User, library libraryRow) error {
	rows, err := s.store.db.QueryContext(ctx, "SELECT entry_id FROM library_folders WHERE library_id = ? ORDER BY position", library.ID)
	if err != nil {
		return err
	}
	var folders []folderRow
	for rows.Next() {
		var folder folderRow
		if err := rows.Scan(&folder.entryID); err != nil {
			_ = rows.Close()
			return err
		}
		folders = append(folders, folder)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	firstScan := library.ScannedAt == ""
	var videos []scannedVideo
	shows := map[string]*scannedShow{}
	for i := range folders {
		folder := &folders[i]
		tree, err := s.files.Tree(ctx, actor, library.SpaceID, folder.entryID)
		if errors.Is(err, files.ErrNotFound) {
			folder.missing = true
			continue
		}
		if err != nil {
			return err
		}
		// Keep the folder's current name and path for display; renaming a
		// folder keeps its entry ID.
		if entry, folderPath, err := s.files.Lookup(ctx, actor, folder.entryID); err == nil {
			if _, err := s.store.db.ExecContext(ctx, "UPDATE library_folders SET name = ?, path = ?, missing = 0 WHERE library_id = ? AND entry_id = ?",
				entry.Name, folderPath, library.ID, folder.entryID); err != nil {
				return err
			}
		}
		found, err := s.readFolder(ctx, actor, library, folder.entryID, tree, shows)
		if err != nil {
			return err
		}
		videos = append(videos, found...)
	}
	for _, folder := range folders {
		if folder.missing {
			if _, err := s.store.db.ExecContext(ctx, "UPDATE library_folders SET missing = 1 WHERE library_id = ? AND entry_id = ?", library.ID, folder.entryID); err != nil {
				return err
			}
		}
	}
	return s.commitScan(ctx, library, videos, shows, firstScan)
}

// readFolder classifies the videos below one library folder.
func (s *Service) readFolder(ctx context.Context, actor accounts.User, library libraryRow, rootEntry string, tree []files.TreeEntry, shows map[string]*scannedShow) ([]scannedVideo, error) {
	filesByDir := map[string][]files.TreeEntry{}
	for _, entry := range tree {
		if entry.Kind != files.EntryKindFile || strings.HasPrefix(entry.Name, ".") {
			continue
		}
		directory := path.Dir(entry.Path)
		if directory == "." {
			directory = ""
		}
		if hiddenPath(directory) {
			continue
		}
		filesByDir[directory] = append(filesByDir[directory], entry)
	}
	existing, err := s.existingNFO(ctx, library.ID)
	if err != nil {
		return nil, err
	}
	var rootName string
	if entry, _, err := s.files.Lookup(ctx, actor, rootEntry); err == nil {
		rootName = entry.Name
	}
	var videos []scannedVideo
	for directory, entries := range filesByDir {
		byName := map[string]files.TreeEntry{}
		var dirVideos []files.TreeEntry
		for _, entry := range entries {
			byName[strings.ToLower(entry.Name)] = entry
			if isVideo(entry.Name) {
				dirVideos = append(dirVideos, entry)
			}
		}
		for _, entry := range dirVideos {
			video := scannedVideo{entry: entry, rootEntry: rootEntry, folder: directory}
			s.classify(ctx, actor, library, &video, directory, rootName, byName, len(dirVideos) == 1, filesByDir, existing, shows)
			videos = append(videos, video)
		}
	}
	return videos, nil
}

func hiddenPath(directory string) bool {
	for _, part := range strings.Split(directory, "/") {
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "@") || part == "#recycle" {
			return true
		}
	}
	return false
}

// sidecar finds the first existing file among names (lower case).
func sidecar(byName map[string]files.TreeEntry, names ...string) (files.TreeEntry, bool) {
	for _, name := range names {
		if entry, ok := byName[name]; ok {
			return entry, true
		}
	}
	return files.TreeEntry{}, false
}

func imageNames(stems ...string) []string {
	var names []string
	for _, stem := range stems {
		for extension := range imageExtensions {
			names = append(names, stem+extension)
		}
	}
	// Map order is random; keep .jpg first for stable choices.
	ordered := make([]string, 0, len(names))
	for _, preferred := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		for _, name := range names {
			if strings.HasSuffix(name, preferred) {
				ordered = append(ordered, name)
			}
		}
	}
	return ordered
}

func (s *Service) classify(ctx context.Context, actor accounts.User, library libraryRow, video *scannedVideo, directory, rootName string,
	byName map[string]files.TreeEntry, alone bool, filesByDir map[string][]files.TreeEntry, existing map[string]storedNFO, shows map[string]*scannedShow) {
	base := baseName(video.entry.Name)
	lowerBase := strings.ToLower(base)
	var parts []string
	if directory != "" {
		parts = strings.Split(directory, "/")
	}
	folderSeason, inSeasonFolder := 0, false
	if len(parts) > 0 {
		folderSeason, inSeasonFolder = seasonOfFolder(parts[len(parts)-1])
	}

	nfoNames := []string{lowerBase + ".nfo"}
	if alone && !inSeasonFolder {
		nfoNames = append(nfoNames, "movie.nfo")
	}
	var document nfoFields
	if entry, ok := sidecar(byName, nfoNames...); ok {
		video.nfoEntry, video.nfoModified = entry.ID, formatTime(entry.ModifiedAt)
		document, video.nfo = s.readNFO(ctx, actor, entry, existing)
	}
	kind, mode := kindOf(library.Kind, base, parts, inSeasonFolder, document)
	video.kind = kind

	// Kodi and Plex sidecars: <name>-poster.jpg and friends, and in a folder
	// that holds a single film also poster.jpg, folder.jpg and fanart.jpg.
	// An episode's own image is its thumbnail.
	if kind == TypeEpisode {
		if entry, ok := sidecar(byName, imageNames(lowerBase+"-thumb", lowerBase)...); ok {
			video.thumb = entry.ID
		}
	} else {
		posterNames := imageNames(lowerBase+"-poster", lowerBase+"-cover", lowerBase)
		backdropNames := imageNames(lowerBase+"-fanart", lowerBase+"-backdrop", lowerBase+"-background")
		if alone {
			posterNames = append(posterNames, imageNames("poster", "folder", "cover", "movie")...)
			backdropNames = append(backdropNames, imageNames("fanart", "backdrop", "background", "landscape")...)
		}
		if entry, ok := sidecar(byName, posterNames...); ok {
			video.poster = entry.ID
		}
		if entry, ok := sidecar(byName, backdropNames...); ok {
			video.backdrop = entry.ID
		}
		if entry, ok := sidecar(byName, imageNames(lowerBase+"-thumb", lowerBase+"-landscape")...); ok {
			video.thumb = entry.ID
		}
	}
	video.subtitles = externalSubtitles(byName, lowerBase)

	switch kind {
	case TypeOther:
		video.title = strings.TrimSpace(base)
	case TypeMovie:
		parsed := parseName(base, mode)
		video.title, video.year = parsed.Title, parsed.Year
		if len(parts) > 0 {
			folderName, folderYear := folderTitle(parts[len(parts)-1])
			if video.title == "" {
				video.title, video.year = folderName, folderYear
			} else if video.year == 0 && normalizeTitle(folderName) == normalizeTitle(video.title) {
				video.year = folderYear
			}
		}
		if video.title == "" {
			video.title = strings.TrimSpace(base)
		}
	case TypeEpisode:
		s.placeEpisode(ctx, actor, video, base, mode, parts, rootName, folderSeason, inSeasonFolder, filesByDir, existing, shows)
		if document.Root == "episodedetails" && document.Episode != 0 {
			video.season, video.episode = document.Season, document.Episode
		}
	}
	if document.Title != "" {
		video.title = document.Title
	}
	if document.Year != 0 && kind != TypeEpisode {
		video.year = document.Year
	}
	video.originalTitle, video.plot, video.genres = document.OriginalTitle, document.Plot, document.Genres
	video.rating, video.collection = document.Rating, document.Set
}

// kindOf decides whether a video is a film, an episode or another video.
// Only mixed libraries look at the name.
func kindOf(library LibraryKind, base string, parts []string, inSeasonFolder bool, document nfoFields) (TitleType, nameMode) {
	switch library {
	case LibraryMovies:
		return TypeMovie, nameMovie
	case LibraryShows:
		return TypeEpisode, nameShow
	case LibraryOther:
		return TypeOther, nameAuto
	}
	parsed := parseName(base, nameAuto)
	folderYear := 0
	if len(parts) > 0 {
		_, folderYear = folderTitle(parts[len(parts)-1])
	}
	switch {
	case document.Root == "episodedetails" || parsed.Marked || inSeasonFolder:
		return TypeEpisode, nameShow
	case document.Root == "movie" || (!parsed.Camera && (parsed.Year != 0 || folderYear != 0)):
		return TypeMovie, nameMovie
	}
	return TypeOther, nameAuto
}

// placeEpisode finds an episode's show, season and number. The show is the
// folder that holds the episode (above a season folder), unless the file
// name names a different show, as in a downloads folder.
func (s *Service) placeEpisode(ctx context.Context, actor accounts.User, video *scannedVideo, base string, mode nameMode, parts []string, rootName string,
	folderSeason int, inSeasonFolder bool, filesByDir map[string][]files.TreeEntry, existing map[string]storedNFO, shows map[string]*scannedShow) {
	parsed := parseName(base, mode)
	video.episode, video.episodeTitle = parsed.Episode, parsed.EpisodeTitle
	season, known := parsed.Season, parsed.HasSeason
	if !known && inSeasonFolder {
		season, known = folderSeason, true
	}
	showParts := parts
	if inSeasonFolder {
		showParts = parts[:len(parts)-1]
	}
	var folderName string
	var folderYear int
	if len(showParts) > 0 {
		folderName, folderYear = folderTitle(showParts[len(showParts)-1])
	} else if rootName != "" {
		folderName, folderYear = folderTitle(rootName)
	}
	// "庆余年 第二季" names the season in the show folder.
	if match := chineseSeason.FindStringSubmatch(folderName); match != nil {
		if !known {
			season, known = chineseNumber(match[1]), true
		}
		folderName = cleanTitle(chineseSeason.ReplaceAllString(folderName, ""))
	}
	if !known {
		season = 1
	}
	video.season = season
	showTitle := parsed.Title
	switch {
	case showTitle == "":
		showTitle = folderName
	case folderName != "" && (strings.Contains(normalizeTitle(folderName), normalizeTitle(showTitle)) || strings.Contains(normalizeTitle(showTitle), normalizeTitle(folderName))):
		showTitle = folderName
	}
	if showTitle == "" {
		showTitle = strings.TrimSpace(base)
	}
	video.title = video.episodeTitle
	key := normalizeTitle(showTitle)
	if key == "" {
		key = showTitle
	}
	video.showKey = key
	show := shows[key]
	if show == nil {
		show = &scannedShow{key: key, title: showTitle, year: folderYear}
		shows[key] = show
	}
	if show.poster != "" && show.backdrop != "" && show.nfoEntry != "" {
		return
	}
	// Show artwork and tvshow.nfo live in the show folder, or in the library
	// folder for a show without one.
	showFiles := map[string]files.TreeEntry{}
	for _, entry := range filesByDir[strings.Join(showParts, "/")] {
		showFiles[strings.ToLower(entry.Name)] = entry
	}
	if show.poster == "" {
		if entry, ok := sidecar(showFiles, imageNames("poster", "folder", "cover", "show")...); ok {
			show.poster = entry.ID
		}
	}
	if show.backdrop == "" {
		if entry, ok := sidecar(showFiles, imageNames("fanart", "backdrop", "background", "landscape")...); ok {
			show.backdrop = entry.ID
		}
	}
	if show.nfoEntry == "" {
		if entry, ok := sidecar(showFiles, "tvshow.nfo"); ok {
			show.nfoEntry, show.nfoModified = entry.ID, formatTime(entry.ModifiedAt)
			show.nfo, show.nfoJSON = s.readNFO(ctx, actor, entry, existing)
			if show.nfo.Title != "" {
				show.title = show.nfo.Title
			}
			if show.nfo.Year != 0 {
				show.year = show.nfo.Year
			}
		}
	}
}

// externalSubtitles finds subtitle files named after the video, as in
// "Movie.srt", "Movie.chs.ass" or "Movie [en].vtt".
func externalSubtitles(byName map[string]files.TreeEntry, lowerBase string) []externalSubtitle {
	var subtitles []externalSubtitle
	for lower, entry := range byName {
		extension := path.Ext(lower)
		format, ok := subtitleExtensions[extension]
		if !ok || !strings.HasPrefix(lower, lowerBase) {
			continue
		}
		marker := strings.TrimSuffix(lower[len(lowerBase):], extension)
		if marker != "" && !strings.ContainsAny(marker[:1], "._- [(") {
			continue
		}
		language, label := subtitleLanguage(marker)
		if label == "" {
			label = strings.Trim(marker, "._- []()")
		}
		if label == "" {
			label = "外挂字幕"
		}
		subtitles = append(subtitles, externalSubtitle{EntryID: entry.ID, Format: format, Language: language, Label: label})
	}
	sortSubtitles(subtitles)
	return subtitles
}

type storedNFO struct {
	modified string
	fields   string
}

// existingNFO maps NFO entry IDs to what the last scan kept from them.
func (s *Service) existingNFO(ctx context.Context, libraryID string) (map[string]storedNFO, error) {
	stored := map[string]storedNFO{}
	rows, err := s.store.db.QueryContext(ctx, `
SELECT nfo_entry, nfo_modified, nfo FROM videos WHERE library_id = ?1 AND nfo_entry <> ''
UNION ALL SELECT nfo_entry, nfo_modified, nfo FROM shows WHERE library_id = ?1 AND nfo_entry <> ''`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var value storedNFO
		if err := rows.Scan(&id, &value.modified, &value.fields); err != nil {
			return nil, err
		}
		stored[id] = value
	}
	return stored, rows.Err()
}

// readNFO returns an NFO file's fields, reusing the last scan's when the
// file is unchanged. A file that cannot be read is treated as empty.
func (s *Service) readNFO(ctx context.Context, actor accounts.User, entry files.TreeEntry, existing map[string]storedNFO) (nfoFields, string) {
	modified := formatTime(entry.ModifiedAt)
	if stored, ok := existing[entry.ID]; ok && stored.modified == modified && stored.fields != "" {
		var fields nfoFields
		if json.Unmarshal([]byte(stored.fields), &fields) == nil {
			return fields, stored.fields
		}
	}
	if entry.SizeBytes > maxNFO {
		return nfoFields{}, ""
	}
	content, err := s.files.OpenContent(ctx, actor, entry.ID)
	if err != nil {
		return nfoFields{}, ""
	}
	data, err := io.ReadAll(io.LimitReader(content.Reader, maxNFO))
	_ = content.Reader.Close()
	if err != nil {
		return nfoFields{}, ""
	}
	document, err := parseNFO(data)
	fields := nfoFields{Root: "none"}
	if err == nil {
		fields = nfoFields{
			Root: document.Root, Title: strings.TrimSpace(document.Title), OriginalTitle: strings.TrimSpace(document.OriginalTitle),
			Year: document.year(), Plot: document.plot(), Genres: document.genres(), Rating: document.rating(), Set: document.setName(),
			Season: nfoNumber(document.Season), Episode: nfoNumber(document.Episode),
		}
	}
	encoded, _ := json.Marshal(fields)
	return fields, string(encoded)
}

func sortSubtitles(subtitles []externalSubtitle) {
	rank := func(language string) int {
		switch language {
		case "zh-Hans":
			return 0
		case "zh-Hant":
			return 1
		case "":
			return 3
		}
		return 2
	}
	sort.SliceStable(subtitles, func(i, j int) bool {
		if rank(subtitles[i].Language) != rank(subtitles[j].Language) {
			return rank(subtitles[i].Language) < rank(subtitles[j].Language)
		}
		return subtitles[i].Label < subtitles[j].Label
	})
}

type existingVideo struct {
	id       string
	size     int64
	modified string
	addedAt  string
}

// commitScan writes one scan's result in a single transaction.
func (s *Service) commitScan(ctx context.Context, library libraryRow, videos []scannedVideo, shows map[string]*scannedShow, firstScan bool) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing := map[string]existingVideo{}
	rows, err := tx.QueryContext(ctx, "SELECT id, entry_id, size_bytes, modified_at, added_at FROM videos WHERE library_id = ?", library.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var entryID string
		var video existingVideo
		if err := rows.Scan(&video.id, &entryID, &video.size, &video.modified, &video.addedAt); err != nil {
			_ = rows.Close()
			return err
		}
		existing[entryID] = video
	}
	if err := rows.Close(); err != nil {
		return err
	}
	showIDs := map[string]string{}
	showRows, err := tx.QueryContext(ctx, "SELECT id, key FROM shows WHERE library_id = ?", library.ID)
	if err != nil {
		return err
	}
	for showRows.Next() {
		var id, key string
		if err := showRows.Scan(&id, &key); err != nil {
			_ = showRows.Close()
			return err
		}
		showIDs[key] = id
	}
	if err := showRows.Close(); err != nil {
		return err
	}
	now := formatTime(s.now())
	used := map[string]bool{}
	for _, video := range videos {
		if video.kind == TypeEpisode {
			used[video.showKey] = true
		}
	}
	for key, show := range shows {
		if !used[key] {
			continue
		}
		id, ok := showIDs[key]
		if !ok {
			id = s.randomID("show")
			showIDs[key] = id
		}
		genres, _ := json.Marshal(nonNil(show.nfo.Genres))
		if _, err := tx.ExecContext(ctx, `
INSERT INTO shows(id, library_id, key, title, original_title, year, plot, genres, rating, poster_entry, backdrop_entry, nfo_entry, nfo_modified, nfo, added_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET title=excluded.title, original_title=excluded.original_title, year=excluded.year, plot=excluded.plot,
genres=excluded.genres, rating=excluded.rating, poster_entry=excluded.poster_entry, backdrop_entry=excluded.backdrop_entry,
nfo_entry=excluded.nfo_entry, nfo_modified=excluded.nfo_modified, nfo=excluded.nfo`,
			id, library.ID, key, show.title, show.nfo.OriginalTitle, show.year, show.nfo.Plot, string(genres), show.nfo.Rating,
			show.poster, show.backdrop, show.nfoEntry, show.nfoModified, show.nfoJSON, now); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, video := range videos {
		seen[video.entry.ID] = true
		var showID any
		if video.kind == TypeEpisode {
			showID = showIDs[video.showKey]
		}
		genres, _ := json.Marshal(nonNil(video.genres))
		subtitles, _ := json.Marshal(nonNilSubtitles(video.subtitles))
		modified := formatTime(video.entry.ModifiedAt)
		previous, known := existing[video.entry.ID]
		if !known {
			added := now
			if firstScan && video.entry.ModifiedAt.Before(s.now()) && !video.entry.ModifiedAt.IsZero() {
				added = modified
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO videos(id, library_id, entry_id, kind, show_id, season, episode, title, episode_title, original_title, year, plot, genres, rating,
collection, root_entry, folder, file_name, size_bytes, modified_at, added_at, poster_entry, backdrop_entry, thumb_entry, nfo_entry,
nfo_modified, nfo, subtitles)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				s.randomID("video"), library.ID, video.entry.ID, video.kind, showID, video.season, video.episode, video.title, video.episodeTitle,
				video.originalTitle, video.year, video.plot, string(genres), video.rating, video.collection, video.rootEntry, video.folder,
				video.entry.Name, video.entry.SizeBytes, modified, added, video.poster, video.backdrop, video.thumb, video.nfoEntry,
				video.nfoModified, video.nfo, string(subtitles)); err != nil {
				return err
			}
			continue
		}
		changed := previous.size != video.entry.SizeBytes || previous.modified != modified
		if _, err := tx.ExecContext(ctx, `
UPDATE videos SET kind=?, show_id=?, season=?, episode=?, title=?, episode_title=?, original_title=?, year=?, plot=?, genres=?, rating=?,
collection=?, root_entry=?, folder=?, file_name=?, size_bytes=?, modified_at=?, poster_entry=?, backdrop_entry=?, thumb_entry=?,
nfo_entry=?, nfo_modified=?, nfo=?, subtitles=?,
probe_state = CASE WHEN ? THEN 'pending' ELSE probe_state END,
frame_state = CASE WHEN ? THEN 'pending' ELSE frame_state END
WHERE id = ?`,
			video.kind, showID, video.season, video.episode, video.title, video.episodeTitle, video.originalTitle, video.year, video.plot,
			string(genres), video.rating, video.collection, video.rootEntry, video.folder, video.entry.Name, video.entry.SizeBytes, modified,
			video.poster, video.backdrop, video.thumb, video.nfoEntry, video.nfoModified, video.nfo, string(subtitles), changed, changed,
			previous.id); err != nil {
			return err
		}
	}
	var removed []string
	for entryID, video := range existing {
		if !seen[entryID] {
			removed = append(removed, video.id)
			if _, err := tx.ExecContext(ctx, "DELETE FROM videos WHERE id = ?", video.id); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM shows WHERE library_id = ? AND id NOT IN (SELECT show_id FROM videos WHERE show_id IS NOT NULL)", library.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dropFrames(removed)
	return nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilSubtitles(values []externalSubtitle) []externalSubtitle {
	if values == nil {
		return []externalSubtitle{}
	}
	return values
}

func (s *Service) videoIDs(ctx context.Context, where string, args ...any) ([]string, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT id FROM videos WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// scanAll scans every library with a session that can read it.
func (s *Service) scanAll(ctx context.Context) {
	libraries, err := s.allLibraries(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "list media libraries", "error", err)
		return
	}
	for _, library := range libraries {
		session, ok := s.sessionFor(ctx, library)
		if !ok {
			continue
		}
		if err := s.scan(asUser(ctx, session), session.User, library, false); err != nil && !errors.Is(err, files.ErrVolumeUnavailable) && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "media library scan failed", "library_id", library.ID, "error", err)
		}
	}
}
