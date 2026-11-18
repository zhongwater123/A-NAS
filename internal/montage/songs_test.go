package montage_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// A test song is text: "song <seconds> <name>", read by songMedia as audio
// of that length. "picture" is no audio at all, and "garbled" audio FFmpeg
// cannot decode.
func songBytes(seconds int, name string) []byte {
	return fmt.Appendf(nil, "song %d %s", seconds, name)
}

type songMedia struct {
	mu      sync.Mutex
	down    bool
	decoded []ffmpegworker.AudioRequest
}

func readAllFrom(file *os.File) string {
	data, _ := io.ReadAll(io.NewSectionReader(file, 0, 1<<20))
	return string(data)
}

func (m *songMedia) Probe(_ context.Context, file *os.File) (ffmpegworker.Probe, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return ffmpegworker.Probe{}, fmt.Errorf("%w: connection refused", ffmpegworker.ErrUnavailable)
	}
	fields := strings.Fields(readAllFrom(file))
	if len(fields) < 2 || fields[0] != "song" {
		return ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Name: "png_pipe"},
			Streams: []ffmpegworker.ProbeStream{{Type: "video", Codec: "png"}}}, nil
	}
	seconds, _ := strconv.Atoi(fields[1])
	return ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Name: "mp3", Duration: float64(seconds)},
		Streams: []ffmpegworker.ProbeStream{{Type: "audio", Codec: "mp3"}}}, nil
}

func (m *songMedia) Audio(_ context.Context, file *os.File, request ffmpegworker.AudioRequest) (io.ReadCloser, error) {
	m.mu.Lock()
	m.decoded = append(m.decoded, request)
	m.mu.Unlock()
	content := readAllFrom(file)
	if strings.Contains(content, "garbled") {
		return nil, fmt.Errorf("%w: invalid data", ffmpegworker.ErrUnreadable)
	}
	if !request.PCM {
		return io.NopCloser(strings.NewReader(fmt.Sprintf("flac from %g of %s", request.Start, content))), nil
	}
	// A few frames of the right width stand in for the samples.
	return io.NopCloser(bytes.NewReader(make([]byte, 4*request.Channels*64))), nil
}

// analyzer stands in for allin1: it reads both streams as the Worker does,
// and finds a downbeat every two seconds.
type analyzer struct {
	mu    sync.Mutex
	down  bool
	songs int
	read  []int
}

func (a *analyzer) Analyze(_ context.Context, stereo, mono *os.File) (montage.Analysis, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.down {
		return montage.Analysis{}, fmt.Errorf("%w: connection refused", montage.ErrWorkerUnavailable)
	}
	for _, stream := range []*os.File{stereo, mono} {
		data, err := io.ReadAll(stream)
		if err != nil {
			return montage.Analysis{}, err
		}
		a.read = append(a.read, len(data))
	}
	a.songs++
	var downbeats []float64
	for t := 0.0; t <= 200; t += 2 {
		downbeats = append(downbeats, t)
	}
	return montage.Analysis{Structure: montage.Structure{Model: "stand-in", BPM: 120, Beats: downbeats, Downbeats: downbeats,
		Segments: []montage.Section{{Start: 0, End: 40, Label: "verse"}, {Start: 40, End: 80, Label: "chorus"}, {Start: 80, End: 200, Label: "outro"}}},
		Loudness: []float32{0.1, 0.2, 0.1}}, nil
}

// cutter stands in for the compute Worker's music operation.
type cutter struct {
	mu       sync.Mutex
	down     bool
	unusable string
	chorus   bool
	asked    []montage.MusicRequest
}

func (c *cutter) Music(_ context.Context, request montage.MusicRequest) (montage.Selection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return montage.Selection{}, fmt.Errorf("%w: connection refused", montage.ErrWorkerUnavailable)
	}
	c.asked = append(c.asked, request)
	start := 40.0
	if request.Start != nil {
		start = *request.Start
	}
	if c.unusable != "" || start > 150 {
		reason := c.unusable
		if reason == "" {
			reason = montage.UnusableNoExcerpt
		}
		return montage.Selection{Unusable: reason, Chorus: c.chorus}, nil
	}
	return montage.Selection{Tempo: 120, Bar: 2, Excerpt: [2]float64{start, start + request.Target}, Seconds: request.Target, Chorus: c.chorus,
		Parts: []montage.Part{{Start: 0, End: request.Target, Level: "高潮", Label: "chorus", Slots: [][2]float64{{0, 2}, {2, request.Target}}}},
		Music: fmt.Appendf(nil, `{"song":%q,"excerpt":[%g,%g]}`, request.Song, start, start+request.Target)}, nil
}

type songFixture struct {
	store   *montage.Store
	lib     *library
	root    string
	media   *songMedia
	music   *analyzer
	compute *cutter
	project string
}

func newSongFixture(t *testing.T) *songFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "montage")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &songFixture{lib: newLibrary(), root: root, media: &songMedia{}, music: &analyzer{}, compute: &cutter{chorus: true}}
	f.lib.add("asset:a1", "user:alice", "object:1", 2)
	f.lib.add("asset:a2", "user:alice", "object:2", 3)
	f.lib.add("asset:b1", "user:bob", "object:9", 3)
	f.store = f.open(t)
	project, _, err := f.store.CreateProject(context.Background(), "user:alice", "alice", "key:1", "", []string{"asset:a1"})
	if err != nil {
		t.Fatal(err)
	}
	f.project = project.ID
	return f
}

func (f *songFixture) open(t *testing.T) *montage.Store {
	t.Helper()
	store, err := montage.Open(context.Background(), f.root, montage.Options{Grants: f.lib, Media: f.media, Music: f.music, Compute: f.compute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func (f *songFixture) put(t *testing.T, owner, project string, song []byte) (montage.SongDetail, error) {
	t.Helper()
	return f.store.PutSong(context.Background(), owner, project, montage.SongSource{TrackID: "track", Title: "Symphony", ContentType: "audio/mpeg"},
		bytes.NewReader(song))
}

func (f *songFixture) copies(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.root, "songs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() != ".incoming" {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestAChosenSongIsCopiedAndAnalysed(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	song, err := f.put(t, "user:alice", f.project, songBytes(200, "symphony"))
	if err != nil {
		t.Fatal(err)
	}
	if song.State != montage.SongAnalyzing || song.Title != "Symphony" || song.Duration != 200 || song.Transcode {
		t.Fatalf("song = %+v", song)
	}
	copies := f.copies(t)
	if len(copies) != 1 {
		t.Fatalf("copies = %v", copies)
	}
	stored, err := os.ReadFile(filepath.Join(f.root, "songs", copies[0], "audio"))
	if err != nil || string(stored) != "song 200 symphony" {
		t.Fatalf("copy = %q, %v", stored, err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	// Stereo at 44.1 kHz for allin1, then mono at 22050 Hz for the loudness,
	// read one after the other.
	if len(f.media.decoded) != 2 || f.media.decoded[0] != (ffmpegworker.AudioRequest{PCM: true, Bits: 32, Channels: 2, SampleRate: 44100}) ||
		f.media.decoded[1] != (ffmpegworker.AudioRequest{PCM: true, Bits: 32, Channels: 1, SampleRate: 22050}) {
		t.Fatalf("decoded = %+v", f.media.decoded)
	}
	if len(f.music.read) != 2 || f.music.read[0] != 512 || f.music.read[1] != 256 {
		t.Fatalf("the analyzer read %v bytes", f.music.read)
	}
	song, err = f.store.Song(ctx, "user:alice", f.project)
	if err != nil {
		t.Fatal(err)
	}
	if song.State != montage.SongReady || song.Selection == nil || song.Selection.Excerpt != [2]float64{40, 130} ||
		song.DefaultStart == nil || *song.DefaultStart != 40 || len(song.Downbeats) != 101 || len(song.Sections) != 3 || !song.Selection.Chorus {
		t.Fatalf("song = %+v", song)
	}
	if asked := f.compute.asked; len(asked) != 1 || asked[0].Start != nil || asked[0].Target != montage.SongTarget || len(asked[0].Loudness) != 3 {
		t.Fatalf("asked = %+v", asked)
	}
}

func TestASongThatIsTooLargeOrNotAudioOrTooLongIsRefused(t *testing.T) {
	f := newSongFixture(t)
	big := append(songBytes(200, "big"), bytes.Repeat([]byte{' '}, montage.MaxSongBytes)...)
	for name, c := range map[string]struct {
		song []byte
		want error
	}{
		"larger than 100 MiB": {big, montage.ErrSongTooLarge},
		"not audio":           {[]byte("picture"), montage.ErrNotAudio},
		"empty":               {nil, montage.ErrNotAudio},
		"shorter than 30 s":   {songBytes(29, "short"), montage.ErrSongLength},
		"longer than 15 min":  {songBytes(901, "long"), montage.ErrSongLength},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.put(t, "user:alice", f.project, c.song); !errors.Is(err, c.want) {
				t.Fatalf("PutSong() = %v; want %v", err, c.want)
			}
		})
	}
	if copies := f.copies(t); len(copies) != 0 {
		t.Fatalf("a refused song left %v", copies)
	}
	if _, err := f.store.Song(context.Background(), "user:alice", f.project); !errors.Is(err, montage.ErrNoSong) {
		t.Fatalf("Song() = %v; want no song", err)
	}
	f.media.down = true
	if _, err := f.put(t, "user:alice", f.project, songBytes(200, "x")); !errors.Is(err, montage.ErrMediaUnavailable) {
		t.Fatalf("PutSong() without the media worker = %v", err)
	}
}

func TestAnotherMembersProjectTakesNoSong(t *testing.T) {
	f := newSongFixture(t)
	if _, err := f.put(t, "user:bob", f.project, songBytes(200, "x")); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("PutSong() = %v; want not found", err)
	}
	if _, err := f.store.Song(context.Background(), "user:bob", f.project); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("Song() = %v; want not found", err)
	}
	if len(f.copies(t)) != 0 {
		t.Fatal("a refused song was kept")
	}
}

func TestDraggingTheStartSnapsToADownbeat(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	if _, err := f.put(t, "user:alice", f.project, songBytes(200, "x")); err != nil {
		t.Fatal(err)
	}
	start := 21.3
	if _, err := f.store.UpdateProject(ctx, "user:alice", f.project, montage.ProjectChange{SongStart: &start}); !errors.Is(err, montage.ErrSongNotReady) {
		t.Fatalf("a start before the analysis = %v", err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateProject(ctx, "user:alice", f.project, montage.ProjectChange{SongStart: &start}); err != nil {
		t.Fatal(err)
	}
	song, err := f.store.Song(ctx, "user:alice", f.project)
	if err != nil {
		t.Fatal(err)
	}
	// 21.3 s is nearest the downbeat at 22 s; the film starts there and
	// lasts as long as the excerpt.
	if last := f.compute.asked[len(f.compute.asked)-1]; last.Start == nil || *last.Start != 22 {
		t.Fatalf("asked = %+v", last)
	}
	if song.Selection.Excerpt != [2]float64{22, 112} || song.Selection.Seconds != 90 || *song.DefaultStart != 40 {
		t.Fatalf("song = %+v", song.Selection)
	}
	// A start that leaves no usable excerpt changes nothing.
	late := 170.0
	if _, err := f.store.UpdateProject(ctx, "user:alice", f.project, montage.ProjectChange{SongStart: &late}); !errors.Is(err, montage.ErrSongStart) {
		t.Fatalf("a late start = %v", err)
	}
	if song, _ := f.store.Song(ctx, "user:alice", f.project); song.Selection.Excerpt[0] != 22 {
		t.Fatalf("after a refused start: %+v", song.Selection)
	}
	// Choosing the song again starts from its default.
	if song, err := f.put(t, "user:alice", f.project, songBytes(200, "x")); err != nil || song.Selection.Excerpt[0] != 40 {
		t.Fatalf("the same song again = %+v, %v", song.Selection, err)
	}
}

func TestASongWithoutAChorusAsksToListen(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	f.compute.chorus = false
	if _, err := f.put(t, "user:alice", f.project, songBytes(200, "x")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	song, err := f.store.Song(ctx, "user:alice", f.project)
	if err != nil || song.State != montage.SongReady || song.Selection.Chorus {
		t.Fatalf("song = %+v, %v; want ready, without a chorus", song, err)
	}
}

func TestASongThatCannotBeUsedSaysWhy(t *testing.T) {
	ctx := context.Background()
	for _, reason := range []string{montage.UnusableUndecodable, montage.UnusableFewBars, montage.UnusableNoExcerpt, montage.UnusableLongSlot} {
		t.Run(reason, func(t *testing.T) {
			f := newSongFixture(t)
			name := "fine"
			if reason == montage.UnusableUndecodable {
				name = "garbled"
			} else {
				f.compute.unusable = reason
			}
			if _, err := f.put(t, "user:alice", f.project, songBytes(200, name)); err != nil {
				t.Fatal(err)
			}
			if err := f.store.AnalyzeWaiting(ctx); err != nil {
				t.Fatal(err)
			}
			song, err := f.store.Song(ctx, "user:alice", f.project)
			if err != nil || song.State != montage.SongUnusable || song.Reason != reason || song.Selection != nil {
				t.Fatalf("song = %+v, %v; want unusable for %s", song, err, reason)
			}
			start := 50.0
			if _, err := f.store.SongStart(ctx, "user:alice", f.project, start); !errors.Is(err, montage.ErrSongNotReady) {
				t.Fatalf("a start for an unusable song = %v", err)
			}
		})
	}
}

func TestASongWaitsForAWorkerThatIsAway(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	f.music.down = true
	if _, err := f.put(t, "user:alice", f.project, songBytes(200, "x")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	song, _ := f.store.Song(ctx, "user:alice", f.project)
	if song.State != montage.SongAnalyzing || song.Waiting != montage.WaitAnalyzer {
		t.Fatalf("song = %+v", song)
	}
	// allin1's structure is kept while the compute Worker is away, and not
	// found again.
	f.music.down, f.compute.down = false, true
	if err := forceDue(t, f); err != nil {
		t.Fatal(err)
	}
	song, _ = f.store.Song(ctx, "user:alice", f.project)
	if song.State != montage.SongAnalyzing || song.Waiting != montage.WaitCompute || len(song.Downbeats) == 0 || f.music.songs != 1 {
		t.Fatalf("song = %+v after %d analyses", song, f.music.songs)
	}
	f.compute.down = false
	if err := forceDue(t, f); err != nil {
		t.Fatal(err)
	}
	song, _ = f.store.Song(ctx, "user:alice", f.project)
	if song.State != montage.SongReady || f.music.songs != 1 {
		t.Fatalf("song = %+v after %d analyses", song, f.music.songs)
	}
}

func openDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(filepath.Join(root, montage.DatabaseName), sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// forceDue makes the waiting songs due now and analyses them.
func forceDue(t *testing.T, f *songFixture) error {
	t.Helper()
	db := openDB(t, f.root)
	if _, err := db.Exec("UPDATE songs SET next_try = NULL"); err != nil {
		return err
	}
	return f.store.AnalyzeWaiting(context.Background())
}

func TestTheSameSongIsAnalysedOnceForEveryone(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	if _, err := f.put(t, "user:alice", f.project, songBytes(200, "shared")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	bobs, _, err := f.store.CreateProject(ctx, "user:bob", "bob", "key:b", "", []string{"asset:b1"})
	if err != nil {
		t.Fatal(err)
	}
	song, err := f.put(t, "user:bob", bobs.ID, songBytes(200, "shared"))
	if err != nil || song.State != montage.SongReady || f.music.songs != 1 {
		t.Fatalf("bob's song = %+v, %v after %d analyses", song, err, f.music.songs)
	}
	if copies := f.copies(t); len(copies) != 1 {
		t.Fatalf("copies = %v", copies)
	}
	// Alice's project goes; Bob's still uses the copy.
	if err := f.store.DeleteProject(ctx, "user:alice", f.project); err != nil {
		t.Fatal(err)
	}
	if copies := f.copies(t); len(copies) != 1 {
		t.Fatalf("copies after alice's project went = %v", copies)
	}
	if err := f.store.RemoveAccount(ctx, "user:bob"); err != nil {
		t.Fatal(err)
	}
	if copies := f.copies(t); len(copies) != 0 {
		t.Fatalf("copies after both went = %v", copies)
	}
}

func TestACopyNoProjectUsesGoes(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	second, _, err := f.store.CreateProject(ctx, "user:alice", "alice", "key:2", "", []string{"asset:a2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.put(t, "user:alice", f.project, songBytes(200, "first")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.put(t, "user:alice", second.ID, songBytes(200, "first")); err != nil {
		t.Fatal(err)
	}
	first := f.copies(t)
	// One project changes its song: the other still uses the first.
	if _, err := f.put(t, "user:alice", f.project, songBytes(180, "second")); err != nil {
		t.Fatal(err)
	}
	if copies := f.copies(t); len(copies) != 2 {
		t.Fatalf("copies = %v", copies)
	}
	if _, err := f.put(t, "user:alice", second.ID, songBytes(180, "second")); err != nil {
		t.Fatal(err)
	}
	if copies := f.copies(t); len(copies) != 1 || copies[0] == first[0] {
		t.Fatalf("copies = %v; the first song's should have gone", copies)
	}
	// What a stop left behind goes when the service starts again.
	if err := os.MkdirAll(filepath.Join(f.root, "songs", ".incoming"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "songs", ".incoming", "song-1"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := strings.Repeat("a", 64)
	if err := os.MkdirAll(filepath.Join(f.root, "songs", orphan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReclaimSongs(ctx); err != nil {
		t.Fatal(err)
	}
	if copies := f.copies(t); len(copies) != 1 || copies[0] == orphan {
		t.Fatalf("copies after reclaiming = %v", copies)
	}
	if _, err := os.Stat(filepath.Join(f.root, "songs", ".incoming")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("songs being received were left: %v", err)
	}
}

func TestTheCopyOutlivesTheOriginal(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	original := filepath.Join(t.TempDir(), "Symphony.mp3")
	if err := os.WriteFile(original, songBytes(200, "symphony"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.PutSong(ctx, "user:alice", f.project, montage.SongSource{Title: "Symphony", ContentType: "audio/mpeg"}, file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	// The member deletes the file from the music library.
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	audio, err := f.store.OpenSongAudio(ctx, "user:alice", f.project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = audio.File.Close() }()
	if data, _ := io.ReadAll(audio.File); string(data) != "song 200 symphony" || audio.ContentType != "audio/mpeg" {
		t.Fatalf("copy = %q as %q", data, audio.ContentType)
	}
	if song, _ := f.store.Song(ctx, "user:alice", f.project); song.State != montage.SongReady {
		t.Fatalf("song = %+v", song)
	}
}

func TestASongBrowsersCannotPlayIsDecodedToFLAC(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	if _, err := f.store.PutSong(ctx, "user:alice", f.project, montage.SongSource{Title: "APE"}, bytes.NewReader(songBytes(200, "ape"))); err != nil {
		t.Fatal(err)
	}
	song, err := f.store.Song(ctx, "user:alice", f.project)
	if err != nil || !song.Transcode {
		t.Fatalf("song = %+v, %v", song, err)
	}
	body, err := f.store.DecodeSong(ctx, "user:alice", f.project, 12.5)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	if data, _ := io.ReadAll(body); string(data) != "flac from 12.5 of song 200 ape" {
		t.Fatalf("decoded = %q", data)
	}
	if _, err := f.store.DecodeSong(ctx, "user:alice", f.project, 300); !errors.Is(err, montage.ErrInvalid) {
		t.Fatalf("decoding past the end = %v", err)
	}
}
