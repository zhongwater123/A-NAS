package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

type videoRow struct {
	ID, LibraryID, EntryID        string
	Kind                          TitleType
	ShowID                        string
	Season, Episode               int
	Title, EpisodeTitle, Original string
	Year                          int
	Plot                          string
	Genres                        []string
	Rating                        float64
	Collection                    string
	RootEntry, Folder, FileName   string
	Size                          int64
	Modified, Added               time.Time
	Poster, Backdrop, Thumb       string
	Subtitles                     []externalSubtitle
	ProbeState, ProbeError        string
	Duration                      float64
	Width, Height                 int
	HDR                           string
	FrameState                    string
}

type showRow struct {
	ID, LibraryID, Title, Original string
	Year                           int
	Plot                           string
	Genres                         []string
	Rating                         float64
	Poster, Backdrop               string
	Added                          time.Time
	episodes                       []*videoRow
	// updated is when the latest episode was added.
	updated time.Time
}

type progressRow struct {
	Position, Duration float64
	Watched            bool
	PlayedAt           time.Time
	InHistory          bool
}

// catalog is everything one user can see, loaded once per request. Libraries
// hold thousands of titles at most, so sorting and filtering happen here.
type catalog struct {
	libraries map[string]libraryRow
	videos    map[string]*videoRow
	order     []*videoRow
	shows     map[string]*showRow
	progress  map[string]progressRow
	favorites map[string]time.Time
}

const videoColumns = `id, library_id, entry_id, kind, COALESCE(show_id, ''), season, episode, title, episode_title, original_title, year, plot,
genres, rating, collection, root_entry, folder, file_name, size_bytes, modified_at, added_at, poster_entry, backdrop_entry, thumb_entry,
subtitles, probe_state, probe_error, duration, width, height, hdr, frame_state`

func scanVideo(row interface{ Scan(...any) error }) (*videoRow, error) {
	var video videoRow
	var genres, subtitles, modified, added string
	if err := row.Scan(&video.ID, &video.LibraryID, &video.EntryID, &video.Kind, &video.ShowID, &video.Season, &video.Episode,
		&video.Title, &video.EpisodeTitle, &video.Original, &video.Year, &video.Plot, &genres, &video.Rating, &video.Collection,
		&video.RootEntry, &video.Folder, &video.FileName, &video.Size, &modified, &added, &video.Poster, &video.Backdrop, &video.Thumb,
		&subtitles, &video.ProbeState, &video.ProbeError, &video.Duration, &video.Width, &video.Height, &video.HDR, &video.FrameState); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(genres), &video.Genres)
	_ = json.Unmarshal([]byte(subtitles), &video.Subtitles)
	video.Modified, video.Added = parseTime(modified), parseTime(added)
	return &video, nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func (s *Service) loadCatalog(ctx context.Context, actor accounts.User) (*catalog, error) {
	libraries, err := s.visibleLibraries(ctx, actor)
	if err != nil {
		return nil, err
	}
	c := &catalog{
		libraries: map[string]libraryRow{}, videos: map[string]*videoRow{}, shows: map[string]*showRow{},
		progress: map[string]progressRow{}, favorites: map[string]time.Time{},
	}
	if len(libraries) == 0 {
		return c, nil
	}
	ids := make([]any, 0, len(libraries))
	for _, library := range libraries {
		c.libraries[library.ID] = library
		ids = append(ids, library.ID)
	}
	in := placeholders(len(ids))
	showRows, err := s.store.db.QueryContext(ctx, `SELECT id, library_id, title, original_title, year, plot, genres, rating, poster_entry, backdrop_entry, added_at
FROM shows WHERE library_id IN (`+in+`)`, ids...)
	if err != nil {
		return nil, err
	}
	for showRows.Next() {
		var show showRow
		var genres, added string
		if err := showRows.Scan(&show.ID, &show.LibraryID, &show.Title, &show.Original, &show.Year, &show.Plot, &genres, &show.Rating,
			&show.Poster, &show.Backdrop, &added); err != nil {
			_ = showRows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(genres), &show.Genres)
		show.Added = parseTime(added)
		c.shows[show.ID] = &show
	}
	if err := showRows.Close(); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT "+videoColumns+" FROM videos WHERE library_id IN ("+in+")", ids...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		video, err := scanVideo(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.videos[video.ID] = video
		c.order = append(c.order, video)
		if show := c.shows[video.ShowID]; show != nil && video.Kind == TypeEpisode {
			show.episodes = append(show.episodes, video)
			if video.Added.After(show.updated) {
				show.updated = video.Added
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, show := range c.shows {
		sortEpisodes(show.episodes)
	}
	progress, err := s.store.db.QueryContext(ctx, "SELECT video_id, position, duration, watched, played_at, in_history FROM progress WHERE user_id = ?", actor.ID)
	if err != nil {
		return nil, err
	}
	for progress.Next() {
		var id, played string
		var row progressRow
		if err := progress.Scan(&id, &row.Position, &row.Duration, &row.Watched, &played, &row.InHistory); err != nil {
			_ = progress.Close()
			return nil, err
		}
		row.PlayedAt = parseTime(played)
		c.progress[id] = row
	}
	if err := progress.Close(); err != nil {
		return nil, err
	}
	favorites, err := s.store.db.QueryContext(ctx, "SELECT target_id, created_at FROM favorites WHERE user_id = ?", actor.ID)
	if err != nil {
		return nil, err
	}
	defer favorites.Close()
	for favorites.Next() {
		var id, created string
		if err := favorites.Scan(&id, &created); err != nil {
			return nil, err
		}
		c.favorites[id] = parseTime(created)
	}
	return c, favorites.Err()
}

func sortEpisodes(episodes []*videoRow) {
	sort.SliceStable(episodes, func(i, j int) bool {
		a, b := episodes[i], episodes[j]
		if a.Season != b.Season {
			// Specials (season 0) come last.
			if a.Season == 0 || b.Season == 0 {
				return b.Season == 0
			}
			return a.Season < b.Season
		}
		if (a.Episode == 0) != (b.Episode == 0) {
			return b.Episode == 0
		}
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		return a.FileName < b.FileName
	})
}

func resolution(width, height int) string {
	switch {
	case width >= 3800 || height >= 2100:
		return "4K"
	case width >= 1900 || height >= 1060:
		return "1080p"
	case width >= 1260 || height >= 700:
		return "720p"
	case height > 0:
		return "SD"
	}
	return ""
}

func artworkVersion(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:4])
}

func (v *videoRow) hasFrame() bool { return v.FrameState == "ready" }

func (v *videoRow) artwork() Artwork {
	image := v.hasFrame() || v.Thumb != "" || v.Backdrop != ""
	return Artwork{
		Poster: v.Poster != "", Backdrop: image, Thumb: image,
		Version: artworkVersion(v.Poster, v.Backdrop, v.Thumb, v.FrameState, v.Modified.String()),
	}
}

// still is the episode whose image stands for the show when the show has
// no artwork of its own.
func (s *showRow) still() *videoRow {
	for _, episode := range s.episodes {
		if episode.hasFrame() || episode.Thumb != "" {
			return episode
		}
	}
	return nil
}

func (s *showRow) artwork() Artwork {
	still := s.still()
	image := s.Backdrop != "" || still != nil
	stillID := ""
	if still != nil {
		stillID = still.ID + still.FrameState + still.Thumb
	}
	return Artwork{Poster: s.Poster != "", Backdrop: image, Thumb: image, Version: artworkVersion(s.Poster, s.Backdrop, stillID)}
}

func (c *catalog) progressOf(id string) *Progress {
	row, ok := c.progress[id]
	if !ok || (row.Position <= 0 && !row.Watched) {
		return nil
	}
	progress := &Progress{Position: row.Position, Duration: row.Duration, Watched: row.Watched}
	if !row.PlayedAt.IsZero() {
		played := row.PlayedAt
		progress.PlayedAt = &played
	}
	return progress
}

func (c *catalog) videoTitle(v *videoRow) Title {
	_, favorite := c.favorites[v.ID]
	title := Title{
		ID: v.ID, Type: v.Kind, LibraryID: v.LibraryID, Title: v.Title, OriginalTitle: v.Original, Year: v.Year,
		Genres: v.Genres, Rating: v.Rating, AddedAt: v.Added, Duration: v.Duration, Resolution: resolution(v.Width, v.Height),
		HDR: v.HDR, Artwork: v.artwork(), Favorite: favorite, Progress: c.progressOf(v.ID),
	}
	if v.Kind == TypeEpisode {
		title.Season, title.Episode = v.Season, v.Episode
		if show := c.shows[v.ShowID]; show != nil {
			title.ShowID, title.ShowTitle = show.ID, show.Title
			if !title.Artwork.Thumb && show.artwork().Thumb {
				title.Artwork = show.artwork()
				title.Artwork.Poster = false
			}
		}
		if title.Title == "" {
			title.Title = episodeLabel(v)
		}
	}
	return title
}

func episodeLabel(v *videoRow) string {
	if v.Episode > 0 {
		return "第 " + strconv.Itoa(v.Episode) + " 集"
	}
	return baseName(v.FileName)
}

func (c *catalog) showTitle(show *showRow) Title {
	_, favorite := c.favorites[show.ID]
	seasons := map[int]bool{}
	watched := 0
	var duration float64
	for _, episode := range show.episodes {
		seasons[episode.Season] = true
		if c.progress[episode.ID].Watched {
			watched++
		}
		duration += episode.Duration
	}
	added := show.updated
	if added.IsZero() {
		added = show.Added
	}
	title := Title{
		ID: show.ID, Type: TypeShow, LibraryID: show.LibraryID, Title: show.Title, OriginalTitle: show.Original, Year: show.Year,
		Genres: show.Genres, Rating: show.Rating, AddedAt: added, Artwork: show.artwork(), Favorite: favorite,
		Seasons: len(seasons), Episodes: len(show.episodes), Watched: watched,
	}
	if len(show.episodes) > 0 {
		first := show.episodes[0]
		title.Resolution, title.HDR = resolution(first.Width, first.Height), first.HDR
	}
	return title
}

// titleOf builds the card for a video or a show ID.
func (c *catalog) titleOf(id string) (Title, bool) {
	if video, ok := c.videos[id]; ok {
		return c.videoTitle(video), true
	}
	if show, ok := c.shows[id]; ok {
		return c.showTitle(show), true
	}
	return Title{}, false
}

// nextEpisode is the episode the show's play button starts: one in
// progress, else the one after the last watched, else the first unwatched.
func (c *catalog) nextEpisode(show *showRow) *videoRow {
	var latest *videoRow
	var latestAt time.Time
	for _, episode := range show.episodes {
		row, ok := c.progress[episode.ID]
		if ok && row.PlayedAt.After(latestAt) {
			latest, latestAt = episode, row.PlayedAt
		}
	}
	if latest != nil {
		if row := c.progress[latest.ID]; !row.Watched {
			return latest
		}
		for i, episode := range show.episodes {
			if episode == latest {
				for _, next := range show.episodes[i+1:] {
					if !c.progress[next.ID].Watched {
						return next
					}
				}
			}
		}
	}
	for _, episode := range show.episodes {
		if !c.progress[episode.ID].Watched {
			return episode
		}
	}
	if len(show.episodes) > 0 {
		return show.episodes[0]
	}
	return nil
}

type titleSorter struct {
	collator *collate.Collator
}

func newSorter() titleSorter {
	return titleSorter{collator: collate.New(language.SimplifiedChinese, collate.Loose, collate.Numeric)}
}

func (t titleSorter) sort(items []Title, by string) {
	byName := func(a, b Title) bool {
		if order := t.collator.CompareString(a.Title, b.Title); order != 0 {
			return order < 0
		}
		return a.ID < b.ID
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch by {
		case "name":
			return byName(a, b)
		case "year":
			if a.Year != b.Year {
				return a.Year > b.Year
			}
			return byName(a, b)
		case "rating":
			if a.Rating != b.Rating {
				return a.Rating > b.Rating
			}
			return byName(a, b)
		}
		if !a.AddedAt.Equal(b.AddedAt) {
			return a.AddedAt.After(b.AddedAt)
		}
		return byName(a, b)
	})
}
