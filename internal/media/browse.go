package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

const (
	defaultPage = 60
	maxPage     = 500
	homeRow     = 20
	homeFeature = 6
)

type TitleQuery struct {
	// Category is all, movie, show or other.
	Category  string
	LibraryID string
	Genre     string
	// Sort is added (default), name, year or rating.
	Sort   string
	Search string
	Offset int
	Limit  int
}

type TitleResults struct {
	TitlePage
	// Genres lists the genres of the matching titles, for filtering.
	Genres []string `json:"genres"`
}

func pageBounds(offset, limit, total int) (int, int) {
	if limit <= 0 {
		limit = defaultPage
	}
	if limit > maxPage {
		limit = maxPage
	}
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return offset, end
}

func matches(search string, values ...string) bool {
	if search == "" {
		return true
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), search) {
			return true
		}
	}
	return false
}

// Titles lists films, shows and other videos.
func (s *Service) Titles(ctx context.Context, actor accounts.User, query TitleQuery) (TitleResults, error) {
	switch query.Category {
	case "", "all", "movie", "show", "other":
	default:
		return TitleResults{}, ErrInvalid
	}
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return TitleResults{}, err
	}
	search := strings.ToLower(strings.TrimSpace(query.Search))
	var items []Title
	genres := map[string]int{}
	include := func(title Title, kind TitleType, extra ...string) {
		if query.LibraryID != "" && title.LibraryID != query.LibraryID {
			return
		}
		if query.Category != "" && query.Category != "all" && query.Category != string(kind) {
			return
		}
		if !matches(search, append([]string{title.Title, title.OriginalTitle}, extra...)...) {
			return
		}
		for _, genre := range title.Genres {
			genres[genre]++
		}
		if query.Genre != "" && !containsString(title.Genres, query.Genre) {
			return
		}
		items = append(items, title)
	}
	for _, video := range c.order {
		if video.Kind != TypeEpisode {
			include(c.videoTitle(video), video.Kind, video.FileName)
		}
	}
	for _, show := range c.shows {
		if len(show.episodes) == 0 {
			continue
		}
		var episodeTitles []string
		if search != "" {
			for _, episode := range show.episodes {
				episodeTitles = append(episodeTitles, episode.Title)
			}
		}
		include(c.showTitle(show), TypeShow, episodeTitles...)
	}
	newSorter().sort(items, query.Sort)
	start, end := pageBounds(query.Offset, query.Limit, len(items))
	result := TitleResults{TitlePage: TitlePage{Items: append([]Title{}, items[start:end]...), Total: len(items)}, Genres: []string{}}
	for genre := range genres {
		result.Genres = append(result.Genres, genre)
	}
	sort.Slice(result.Genres, func(i, j int) bool {
		if genres[result.Genres[i]] != genres[result.Genres[j]] {
			return genres[result.Genres[i]] > genres[result.Genres[j]]
		}
		return result.Genres[i] < result.Genres[j]
	})
	return result, nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// continueWatching lists videos in progress and the next episode of shows
// whose last played episode was finished, latest first, one per show.
func (c *catalog) continueWatching() []Title {
	type entry struct {
		title  Title
		played int64
	}
	var entries []entry
	seenShows := map[string]bool{}
	type played struct {
		video *videoRow
		row   progressRow
	}
	var history []played
	for id, row := range c.progress {
		if video, ok := c.videos[id]; ok && !row.PlayedAt.IsZero() {
			history = append(history, played{video, row})
		}
	}
	sort.Slice(history, func(i, j int) bool { return history[i].row.PlayedAt.After(history[j].row.PlayedAt) })
	for _, item := range history {
		video := item.video
		if video.Kind == TypeEpisode {
			if seenShows[video.ShowID] {
				continue
			}
			seenShows[video.ShowID] = true
			show := c.shows[video.ShowID]
			if show == nil {
				continue
			}
			next := c.nextEpisode(show)
			if next == nil || c.progress[next.ID].Watched {
				continue
			}
			entries = append(entries, entry{c.videoTitle(next), item.row.PlayedAt.UnixNano()})
			continue
		}
		if !item.row.Watched && item.row.Position > 0 {
			entries = append(entries, entry{c.videoTitle(video), item.row.PlayedAt.UnixNano()})
		}
	}
	titles := make([]Title, 0, len(entries))
	for _, item := range entries {
		titles = append(titles, item.title)
	}
	return titles
}

func limit(items []Title, count int) []Title {
	if items == nil {
		return []Title{}
	}
	if len(items) > count {
		return items[:count]
	}
	return items
}

// Home is the start page: what to continue, what is new, and each kind.
func (s *Service) Home(ctx context.Context, actor accounts.User) (Home, error) {
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return Home{}, err
	}
	home := Home{Libraries: len(c.libraries)}
	s.mu.Lock()
	for id := range c.libraries {
		home.Scanning = home.Scanning || s.scanning[id]
	}
	s.mu.Unlock()
	var movies, shows, others []Title
	for _, video := range c.order {
		switch video.Kind {
		case TypeMovie:
			movies = append(movies, c.videoTitle(video))
		case TypeOther:
			others = append(others, c.videoTitle(video))
		}
	}
	for _, show := range c.shows {
		if len(show.episodes) > 0 {
			shows = append(shows, c.showTitle(show))
		}
	}
	sorter := newSorter()
	sorter.sort(movies, "added")
	sorter.sort(shows, "added")
	sorter.sort(others, "added")
	recent := append(append(append([]Title{}, movies...), shows...), others...)
	sorter.sort(recent, "added")
	home.Continue = limit(c.continueWatching(), homeRow)
	home.Recent = limit(recent, homeRow)
	home.Movies = limit(movies, homeRow)
	home.Shows = limit(shows, homeRow)
	home.Others = limit(others, homeRow)
	var favorites []Title
	for id := range c.favorites {
		if title, ok := c.titleOf(id); ok {
			favorites = append(favorites, title)
		}
	}
	sort.SliceStable(favorites, func(i, j int) bool { return c.favorites[favorites[i].ID].After(c.favorites[favorites[j].ID]) })
	home.Favorites = limit(favorites, homeRow)
	// The banner shows what the user is watching, then new films and shows,
	// each once and only with a wide image; other videos only when there is
	// nothing else.
	seen := map[string]bool{}
	home.Featured = []Title{}
	feature := func(candidates []Title, others bool) {
		for _, title := range candidates {
			key := title.ID
			if title.ShowID != "" {
				key = title.ShowID
			}
			if len(home.Featured) == homeFeature || seen[key] || !title.Artwork.Backdrop || (title.Type == TypeOther) != others {
				continue
			}
			seen[key] = true
			home.Featured = append(home.Featured, title)
		}
	}
	feature(home.Continue, false)
	feature(recent, false)
	if len(home.Featured) == 0 {
		feature(home.Continue, true)
		feature(recent, true)
	}
	return home, nil
}

func (s *Service) visibleVideo(ctx context.Context, actor accounts.User, id string) (*videoRow, libraryRow, error) {
	video, err := scanVideo(s.store.db.QueryRowContext(ctx, "SELECT "+videoColumns+" FROM videos WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, libraryRow{}, ErrNotFound
	}
	if err != nil {
		return nil, libraryRow{}, err
	}
	library, err := s.library(ctx, actor, video.LibraryID)
	if err != nil {
		return nil, libraryRow{}, err
	}
	return video, library, nil
}

// Video describes one film, episode or other video for its detail page and
// the player.
func (s *Service) Video(ctx context.Context, actor accounts.User, id string) (VideoDetail, error) {
	if _, _, err := s.visibleVideo(ctx, actor, id); err != nil {
		return VideoDetail{}, err
	}
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return VideoDetail{}, err
	}
	video, ok := c.videos[id]
	if !ok {
		return VideoDetail{}, ErrNotFound
	}
	detail := VideoDetail{
		Title: c.videoTitle(video), Plot: video.Plot, ProbeState: video.ProbeState, Collection: video.Collection,
		Audio: []AudioTrack{}, Subtitles: []SubtitleTrack{}, Collections: []CollectionRef{},
	}
	if video.Kind == TypeEpisode {
		detail.EpisodeName = video.EpisodeTitle
		if show := c.shows[video.ShowID]; show != nil {
			if detail.Plot == "" {
				detail.Plot = show.Plot
			}
			for i, episode := range show.episodes {
				if episode.ID == video.ID {
					if i > 0 {
						detail.Previous = show.episodes[i-1].ID
					}
					if i+1 < len(show.episodes) {
						detail.Next = show.episodes[i+1].ID
					}
				}
			}
		}
	}
	folder := video.Folder
	var rootName string
	_ = s.store.db.QueryRowContext(ctx, "SELECT name FROM library_folders WHERE library_id = ? AND entry_id = ?", video.LibraryID, video.RootEntry).Scan(&rootName)
	detail.File = VideoFile{Name: video.FileName, Folder: path.Join(rootName, folder), SizeBytes: video.Size, ModifiedAt: video.Modified}
	info, err := s.mediaInfo(ctx, id)
	if err != nil {
		return VideoDetail{}, err
	}
	if info.Container != "" {
		summary := &MediaSummary{Container: containerLabel(info.Container, video.FileName), Bitrate: info.Bitrate}
		if info.Video != nil {
			summary.VideoCodec, summary.Width, summary.Height, summary.FrameRate = codecLabel(info.Video.Codec), info.Video.Width, info.Video.Height, info.Video.FrameRate
		}
		if len(info.Audio) > 0 {
			summary.AudioCodec = codecLabel(info.Audio[0].Codec)
		}
		detail.Media = summary
	}
	for position, audio := range info.Audio {
		detail.Audio = append(detail.Audio, AudioTrack{
			Index: position, Label: audioLabel(audio), Language: audio.Language, Codec: codecLabel(audio.Codec),
			Channels: audio.Channels, Default: audio.Default,
		})
	}
	detail.Subtitles = subtitleTracks(video, info)
	refs, err := s.collectionRefs(ctx, actor, id)
	if err != nil {
		return VideoDetail{}, err
	}
	detail.Collections = refs
	return detail, nil
}

func (s *Service) mediaInfo(ctx context.Context, videoID string) (MediaInfo, error) {
	var encoded string
	if err := s.store.db.QueryRowContext(ctx, "SELECT probe FROM videos WHERE id = ?", videoID).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MediaInfo{}, ErrNotFound
		}
		return MediaInfo{}, err
	}
	var info MediaInfo
	_ = json.Unmarshal([]byte(encoded), &info)
	return info, nil
}

// textSubtitleCodecs are the embedded subtitles FFmpeg can turn into WebVTT;
// bitmap subtitles (PGS, VobSub) cannot be shown.
var textSubtitleCodecs = map[string]bool{"subrip": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true, "text": true}

func subtitleTracks(video *videoRow, info MediaInfo) []SubtitleTrack {
	tracks := []SubtitleTrack{}
	for i, subtitle := range video.Subtitles {
		tracks = append(tracks, SubtitleTrack{ID: "x" + strconv.Itoa(i), Label: subtitle.Label, Language: subtitle.Language, External: true})
	}
	for _, stream := range info.Subtitles {
		if !textSubtitleCodecs[stream.Codec] {
			continue
		}
		label := stream.Title
		if label == "" {
			label = languageName(stream.Language)
		}
		if label == "" {
			label = "内嵌字幕 " + strconv.Itoa(len(tracks)+1)
		}
		tracks = append(tracks, SubtitleTrack{ID: "s" + strconv.Itoa(stream.Index), Label: label, Language: stream.Language, Default: stream.Default || stream.Forced})
	}
	return tracks
}

func languageName(code string) string {
	switch strings.ToLower(code) {
	case "chi", "zho", "zh", "chs", "zh-cn", "zh-hans", "cmn":
		return "中文"
	case "cht", "zh-tw", "zh-hk", "zh-hant":
		return "繁体中文"
	case "yue", "can":
		return "粤语"
	case "eng", "en":
		return "英语"
	case "jpn", "ja":
		return "日语"
	case "kor", "ko":
		return "韩语"
	case "fre", "fra", "fr":
		return "法语"
	case "ger", "deu", "de":
		return "德语"
	case "spa", "es":
		return "西班牙语"
	case "rus", "ru":
		return "俄语"
	}
	return ""
}

func audioLabel(audio AudioStream) string {
	parts := []string{}
	if audio.Title != "" {
		parts = append(parts, audio.Title)
	} else if name := languageName(audio.Language); name != "" {
		parts = append(parts, name)
	}
	parts = append(parts, codecLabel(audio.Codec))
	switch audio.Channels {
	case 0:
	case 1:
		parts = append(parts, "单声道")
	case 2:
		parts = append(parts, "立体声")
	case 6:
		parts = append(parts, "5.1")
	case 8:
		parts = append(parts, "7.1")
	default:
		parts = append(parts, strconv.Itoa(audio.Channels)+" 声道")
	}
	return strings.Join(parts, " · ")
}

func codecLabel(codec string) string {
	switch codec {
	case "h264":
		return "H.264"
	case "hevc":
		return "HEVC"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "vp8":
		return "VP8"
	case "mpeg4":
		return "MPEG-4"
	case "mpeg2video":
		return "MPEG-2"
	case "aac":
		return "AAC"
	case "ac3":
		return "AC3"
	case "eac3":
		return "E-AC3"
	case "dts":
		return "DTS"
	case "truehd":
		return "TrueHD"
	case "flac":
		return "FLAC"
	case "mp3":
		return "MP3"
	case "opus":
		return "Opus"
	case "vorbis":
		return "Vorbis"
	}
	return strings.ToUpper(codec)
}

func containerLabel(format, name string) string {
	switch {
	case strings.Contains(format, "mp4"):
		if strings.EqualFold(path.Ext(name), ".mov") {
			return "MOV"
		}
		return "MP4"
	case strings.Contains(format, "matroska"):
		if strings.EqualFold(path.Ext(name), ".webm") {
			return "WebM"
		}
		return "MKV"
	}
	if extension := strings.TrimPrefix(path.Ext(name), "."); extension != "" {
		return strings.ToUpper(extension)
	}
	return strings.ToUpper(format)
}

// Show describes a show with its seasons and episodes.
func (s *Service) Show(ctx context.Context, actor accounts.User, id string) (ShowDetail, error) {
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return ShowDetail{}, err
	}
	show, ok := c.shows[id]
	if !ok || len(show.episodes) == 0 {
		return ShowDetail{}, ErrNotFound
	}
	detail := ShowDetail{Title: c.showTitle(show), Plot: show.Plot, SeasonList: []Season{}}
	for _, video := range show.episodes {
		episode := c.episode(video)
		if count := len(detail.SeasonList); count == 0 || detail.SeasonList[count-1].Number != video.Season {
			detail.SeasonList = append(detail.SeasonList, Season{Number: video.Season})
		}
		current := &detail.SeasonList[len(detail.SeasonList)-1]
		current.Episodes = append(current.Episodes, episode)
	}
	if next := c.nextEpisode(show); next != nil {
		episode := c.episode(next)
		detail.Next = &episode
	}
	refs, err := s.collectionRefs(ctx, actor, id)
	if err != nil {
		return ShowDetail{}, err
	}
	detail.Collections = refs
	return detail, nil
}

func (c *catalog) episode(video *videoRow) Episode {
	title := video.Title
	if title == "" {
		title = episodeLabel(video)
	}
	return Episode{
		ID: video.ID, Season: video.Season, Episode: video.Episode, Title: title, Plot: video.Plot, Duration: video.Duration,
		AddedAt: video.Added, Artwork: video.artwork(), Progress: c.progressOf(video.ID),
	}
}

// History lists what the user played, latest first.
func (s *Service) History(ctx context.Context, actor accounts.User, offset, count int) (TitlePage, error) {
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return TitlePage{}, err
	}
	type played struct {
		video *videoRow
		row   progressRow
	}
	var history []played
	for id, row := range c.progress {
		if video, ok := c.videos[id]; ok && row.InHistory && !row.PlayedAt.IsZero() {
			history = append(history, played{video, row})
		}
	}
	sort.Slice(history, func(i, j int) bool { return history[i].row.PlayedAt.After(history[j].row.PlayedAt) })
	start, end := pageBounds(offset, count, len(history))
	page := TitlePage{Items: []Title{}, Total: len(history)}
	for _, item := range history[start:end] {
		page.Items = append(page.Items, c.videoTitle(item.video))
	}
	return page, nil
}

// Favorites lists the user's favorite titles, latest first.
func (s *Service) Favorites(ctx context.Context, actor accounts.User) (TitlePage, error) {
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return TitlePage{}, err
	}
	page := TitlePage{Items: []Title{}}
	for id := range c.favorites {
		if title, ok := c.titleOf(id); ok {
			page.Items = append(page.Items, title)
		}
	}
	sort.SliceStable(page.Items, func(i, j int) bool {
		return c.favorites[page.Items[i].ID].After(c.favorites[page.Items[j].ID])
	})
	page.Total = len(page.Items)
	return page, nil
}

// FolderRoots lists the folders of every visible library.
func (s *Service) FolderRoots(ctx context.Context, actor accounts.User) ([]FolderRoot, error) {
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return nil, err
	}
	libraries, err := s.ListLibraries(ctx, actor)
	if err != nil {
		return nil, err
	}
	roots := []FolderRoot{}
	for _, library := range libraries {
		for _, folder := range library.Folders {
			root := FolderRoot{LibraryID: library.ID, LibraryName: library.Name, EntryID: folder.EntryID, Name: folder.Name, Path: folder.Path, Missing: folder.Missing}
			root.Videos, root.Cover = c.folderSummary(library.ID, folder.EntryID, "")
			roots = append(roots, root)
		}
	}
	return roots, nil
}

func (c *catalog) folderSummary(libraryID, rootEntry, prefix string) (int, string) {
	count := 0
	var cover *videoRow
	for _, video := range c.order {
		if video.LibraryID != libraryID || video.RootEntry != rootEntry || !within(video.Folder, prefix) {
			continue
		}
		count++
		if video.artwork().Thumb && (cover == nil || video.Added.After(cover.Added)) {
			cover = video
		}
	}
	if cover == nil {
		return count, ""
	}
	return count, cover.ID
}

func within(folder, prefix string) bool {
	return prefix == "" || folder == prefix || strings.HasPrefix(folder, prefix+"/")
}

// Folder lists one folder of a library: its subfolders that hold videos,
// and the videos directly in it.
func (s *Service) Folder(ctx context.Context, actor accounts.User, rootEntry, folderPath string) (FolderListing, error) {
	folderPath = strings.Trim(path.Clean("/"+folderPath), "/")
	roots, err := s.FolderRoots(ctx, actor)
	if err != nil {
		return FolderListing{}, err
	}
	var root *FolderRoot
	for i := range roots {
		if roots[i].EntryID == rootEntry {
			root = &roots[i]
		}
	}
	if root == nil {
		return FolderListing{}, ErrNotFound
	}
	c, err := s.loadCatalog(ctx, actor)
	if err != nil {
		return FolderListing{}, err
	}
	listing := FolderListing{Root: *root, Path: folderPath, Folders: []Folder{}, Videos: []Title{}}
	children := map[string]bool{}
	for _, video := range c.order {
		if video.LibraryID != root.LibraryID || video.RootEntry != rootEntry || !within(video.Folder, folderPath) {
			continue
		}
		if video.Folder == folderPath {
			listing.Videos = append(listing.Videos, c.videoTitle(video))
			continue
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(video.Folder, folderPath), "/")
		child, _, _ := strings.Cut(rest, "/")
		children[path.Join(folderPath, child)] = true
	}
	if len(listing.Videos) == 0 && len(children) == 0 && folderPath != "" {
		return FolderListing{}, ErrNotFound
	}
	sorter := newSorter()
	for child := range children {
		folder := Folder{Name: path.Base(child), Path: child}
		folder.Videos, folder.Cover = c.folderSummary(root.LibraryID, rootEntry, child)
		listing.Folders = append(listing.Folders, folder)
	}
	sort.Slice(listing.Folders, func(i, j int) bool {
		return sorter.collator.CompareString(listing.Folders[i].Name, listing.Folders[j].Name) < 0
	})
	sort.SliceStable(listing.Videos, func(i, j int) bool {
		a, b := listing.Videos[i], listing.Videos[j]
		if a.Season != b.Season || a.Episode != b.Episode {
			if a.Season != b.Season {
				return a.Season < b.Season
			}
			return a.Episode < b.Episode
		}
		return sorter.collator.CompareString(c.videos[a.ID].FileName, c.videos[b.ID].FileName) < 0
	})
	return listing, nil
}
