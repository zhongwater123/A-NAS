package music_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/imagetest"
	"github.com/zhongwater123/A-NAS/internal/music"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type acceptingCredentials struct{}

func (acceptingCredentials) SetCredential(context.Context, accounts.CredentialRequest) error {
	return nil
}
func (acceptingCredentials) DisableCredential(context.Context, string) error { return nil }

type fixture struct {
	ctx      context.Context
	library  *music.Service
	files    *files.Service
	admin    accounts.User
	member   accounts.User
	private  string // the admin's personal space directory
	shared   string
	spaceIDs map[string]string
}

func newFixture(t *testing.T) *fixture { return newFixtureWith(t, nil) }

// newFixtureWith gives the library a media worker.
func newFixtureWith(t *testing.T, worker ffmpegworker.Worker) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	accountStore, err := accounts.OpenSQLite(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatalf("open accounts: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("setup administrator: %v", err)
	}
	member, err := accountService.CreateMember(ctx, admin, "kid", "another long passphrase")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	fileStore, err := files.OpenSQLite(filepath.Join(dir, "files.db"))
	if err != nil {
		t.Fatalf("open files: %v", err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	volume := filepath.Join(dir, "volume")
	fileService := files.NewLocalService(fileStore, volume, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	musicStore, err := music.OpenSQLite(filepath.Join(dir, "music.db"))
	if err != nil {
		t.Fatalf("open music: %v", err)
	}
	t.Cleanup(func() { _ = musicStore.Close() })
	library := music.New(musicStore, filepath.Join(dir, "covers"), fileService, accountService, music.Options{Worker: worker})
	t.Cleanup(library.Close)
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatalf("list spaces: %v", err)
	}
	ids := map[string]string{}
	for _, space := range spaces {
		ids[string(space.Kind)] = space.ID
	}
	return &fixture{
		ctx: ctx, library: library, files: fileService, admin: admin, member: member,
		private: filepath.Join(volume, "spaces", "private", "owner"), shared: filepath.Join(volume, "spaces", "shared"),
		spaceIDs: ids,
	}
}

// scan runs a scan for actor and waits for it to finish.
func (f *fixture) scan(t *testing.T, actor accounts.User) music.Status {
	t.Helper()
	if _, err := f.library.StartScan(f.ctx, actor); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, err := f.library.Status(f.ctx, actor)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Scan.State == "idle" {
			if status.Scan.Error != "" {
				t.Fatalf("scan failed: %s", status.Scan.Error)
			}
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan did not finish")
	return music.Status{}
}

func (f *fixture) tracks(t *testing.T, actor accounts.User) map[string]music.Track {
	t.Helper()
	library, err := f.library.Tracks(f.ctx, actor)
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	byTitle := map[string]music.Track{}
	for _, track := range library.Items {
		byTitle[track.Title] = track
	}
	return byTitle
}

func write(t *testing.T, file string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pngImage(t *testing.T, shade uint8) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			picture.Set(x, y, color.RGBA{R: shade, G: uint8(x * 4), B: uint8(y * 5), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

// id3Frame encodes an ID3v2.3 frame.
func id3Frame(id string, body []byte) []byte {
	frame := []byte(id)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(body)))
	frame = append(frame, 0, 0)
	return append(frame, body...)
}

// mp3File is an ID3v2.3 tag followed by silent 128 kbit/s MPEG-1 Layer III
// frames of 1152 samples at 44.1 kHz.
func mp3File(frames int, tagFrames ...[]byte) []byte {
	var body []byte
	for _, frame := range tagFrames {
		body = append(body, frame...)
	}
	size := len(body)
	file := []byte{'I', 'D', '3', 3, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	file = append(file, body...)
	for range frames {
		frame := make([]byte, 417)
		copy(frame, []byte{0xff, 0xfb, 0x90, 0x00})
		file = append(file, frame...)
	}
	return file
}

func text(value string) []byte { return append([]byte{3}, value...) } // UTF-8

func flacFile(t *testing.T, samples uint64, picture []byte, tags ...[2]string) []byte {
	t.Helper()
	info := &meta.StreamInfo{BlockSizeMin: 4096, BlockSizeMax: 4096, SampleRate: 44100, NChannels: 2, BitsPerSample: 16, NSamples: samples}
	blocks := []*meta.Block{{Type: meta.TypeVorbisComment, Length: 1, Body: &meta.VorbisComment{Vendor: "test", Tags: tags}}}
	if picture != nil {
		blocks = append(blocks, &meta.Block{Type: meta.TypePicture, Length: 1, Body: &meta.Picture{Type: 3, MIME: "image/png", Width: 64, Height: 48, Data: picture}})
	}
	var encoded bytes.Buffer
	encoder, err := flac.NewEncoder(&encoded, info, blocks...)
	if err != nil {
		t.Fatalf("encode flac: %v", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("close flac: %v", err)
	}
	return encoded.Bytes()
}

func TestScanIndexesTagsCoversLyricsAndDurations(t *testing.T) {
	f := newFixture(t)
	gbkTitle, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("晴天"))
	gbkArtist, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("周杰伦"))
	cover := pngImage(t, 200)
	// An old tagger's GBK bytes in ISO-8859-1 frames, an embedded cover and lyrics.
	write(t, filepath.Join(f.private, "音乐", "叶惠美", "01 晴天.mp3"), mp3File(100,
		id3Frame("TIT2", append([]byte{0}, gbkTitle...)),
		id3Frame("TPE1", append([]byte{0}, gbkArtist...)),
		id3Frame("TALB", text("叶惠美")),
		id3Frame("TCON", text("流行")),
		id3Frame("TRCK", text("1/11")),
		id3Frame("APIC", append(append([]byte{0}, "image/png\x00"...), append([]byte{3, 0}, cover...)...)),
		id3Frame("USLT", append([]byte{3}, "chi\x00[00:01.00]故事的小黄花"...)),
	))
	// FLAC tags; the cover comes from the folder and the lyrics from an .lrc.
	write(t, filepath.Join(f.shared, "Classical", "Bach", "aria.flac"), flacFile(t, 44100*3, nil,
		[2]string{"TITLE", "Aria"}, [2]string{"ARTIST", "Glenn Gould"}, [2]string{"ALBUM", "Goldberg Variations"},
		[2]string{"DATE", "1981"}, [2]string{"GENRE", "Classical"}, [2]string{"DISCNUMBER", "1"}))
	write(t, filepath.Join(f.shared, "Classical", "Bach", "cover.jpg"), cover)
	write(t, filepath.Join(f.shared, "Classical", "Bach", "aria.lrc"), []byte("[00:00.50]Aria"))
	// Untagged: the file name names artist and title.
	write(t, filepath.Join(f.shared, "陈奕迅 - 十年.mp3"), mp3File(10))
	// Not music, and hidden folders.
	write(t, filepath.Join(f.shared, "notes.txt"), []byte("hello"))
	write(t, filepath.Join(f.shared, ".cache", "hidden.mp3"), mp3File(10))

	status := f.scan(t, f.admin)
	if status.Tracks != 3 {
		t.Fatalf("library holds %d tracks, want 3", status.Tracks)
	}
	tracks := f.tracks(t, f.admin)
	sunny, ok := tracks["晴天"]
	if !ok {
		t.Fatalf("GBK title was not repaired: %v", tracks)
	}
	if sunny.Artist != "周杰伦" || sunny.Album != "叶惠美" || sunny.TrackNo != 1 || sunny.Format != "mp3" || sunny.Folder != "音乐/叶惠美" {
		t.Errorf("mp3 track = %+v", sunny)
	}
	if want := int64(100 * 1152 * 1000 / 44100); sunny.DurationMs < want-1 || sunny.DurationMs > want+1 {
		t.Errorf("mp3 duration = %d ms, want %d", sunny.DurationMs, want)
	}
	if sunny.CoverID == "" || !sunny.Lyrics {
		t.Errorf("mp3 cover %q, lyrics %v", sunny.CoverID, sunny.Lyrics)
	}
	aria := tracks["Aria"]
	if aria.Artist != "Glenn Gould" || aria.Year != 1981 || aria.DurationMs != 3000 || aria.DiscNo != 1 || len(aria.Genres) != 1 {
		t.Errorf("flac track = %+v", aria)
	}
	if aria.CoverID == "" || aria.CoverID != sunny.CoverID {
		t.Errorf("folder cover %q should match the identical embedded picture %q", aria.CoverID, sunny.CoverID)
	}
	ten := tracks["十年"]
	if ten.Artist != "陈奕迅" || ten.Album != "" || ten.AlbumID != "album:unknown" {
		t.Errorf("untagged track = %+v", ten)
	}

	for _, size := range []string{music.CoverSmall, music.CoverLarge} {
		file, err := f.library.CoverPath(f.ctx, f.admin, sunny.CoverID, size)
		if err != nil {
			t.Fatalf("cover %s: %v", size, err)
		}
		rendition, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		config, format, err := image.DecodeConfig(rendition)
		_ = rendition.Close()
		if err != nil || format != "jpeg" || config.Width != 48 || config.Height != 48 {
			t.Errorf("%s cover is %s %dx%d (%v), want a 48 px JPEG square", size, format, config.Width, config.Height, err)
		}
	}
	lyrics, err := f.library.Lyrics(f.ctx, f.admin, aria.ID)
	if err != nil || lyrics != "[00:00.50]Aria" {
		t.Errorf("sidecar lyrics = %q, %v", lyrics, err)
	}
	if lyrics, err := f.library.Lyrics(f.ctx, f.admin, sunny.ID); err != nil || lyrics != "[00:01.00]故事的小黄花" {
		t.Errorf("embedded lyrics = %q, %v", lyrics, err)
	}
	stream, err := f.library.OpenTrack(f.ctx, f.admin, aria.ID)
	if err != nil {
		t.Fatalf("open track: %v", err)
	}
	header := make([]byte, 4)
	_, err = io.ReadFull(stream.Reader, header)
	_ = stream.Reader.Close()
	if err != nil || string(header) != "fLaC" || stream.Track.ContentType() != "audio/flac" {
		t.Errorf("stream starts %q with %q (%v)", header, stream.Track.ContentType(), err)
	}
}

func TestCoversThatPanicTheDecoderLeaveTheScanRunning(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	panicking := imagetest.PanickingPicture()
	f := newFixture(t)
	write(t, filepath.Join(f.shared, "Embedded", "song.mp3"), mp3File(10,
		id3Frame("TIT2", text("Song")),
		id3Frame("APIC", append(append([]byte{0}, "image/png\x00"...), append([]byte{3, 0}, panicking...)...)),
	))
	write(t, filepath.Join(f.shared, "Folder", "aria.flac"), flacFile(t, 44100, nil, [2]string{"TITLE", "Aria"}))
	write(t, filepath.Join(f.shared, "Folder", "cover.jpg"), panicking)

	status := f.scan(t, f.admin)
	if status.Tracks != 2 {
		t.Fatalf("library holds %d tracks, want 2", status.Tracks)
	}
	tracks := f.tracks(t, f.admin)
	for _, title := range []string{"Song", "Aria"} {
		track, ok := tracks[title]
		if !ok || track.CoverID != "" {
			t.Errorf("track %q = %+v (indexed %v), want it indexed without a cover", title, track, ok)
		}
	}
	// Both covers are logged as decoder panics, without the files' names.
	logged := panics()
	if len(logged) != 2 {
		t.Fatalf("logged %d recovered panics, want 2: %+v", len(logged), logged)
	}
	imagetest.RequirePanicLogged(t, logged, "music cover", "song.mp3", "cover.jpg", "Embedded", "Folder")
}

func TestMembersOnlySeeTheirOwnSpaceAndTheSharedSpace(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.private, "mine.mp3"), mp3File(5, id3Frame("TIT2", text("Mine"))))
	write(t, filepath.Join(f.shared, "ours.mp3"), mp3File(5, id3Frame("TIT2", text("Ours"))))
	f.scan(t, f.admin)
	f.scan(t, f.member)
	if tracks := f.tracks(t, f.admin); len(tracks) != 2 {
		t.Errorf("administrator sees %d tracks, want 2", len(tracks))
	}
	memberTracks := f.tracks(t, f.member)
	if _, ok := memberTracks["Mine"]; ok || len(memberTracks) != 1 {
		t.Fatalf("member sees %v, want only the shared track", memberTracks)
	}
	mine := f.tracks(t, f.admin)["Mine"]
	if _, err := f.library.OpenTrack(f.ctx, f.member, mine.ID); !errors.Is(err, music.ErrNotFound) {
		t.Errorf("member opened another member's track: %v", err)
	}
	if err := f.library.SetFavorite(f.ctx, f.member, mine.ID, true); !errors.Is(err, music.ErrNotFound) {
		t.Errorf("member favorited another member's track: %v", err)
	}
	if _, err := f.library.CoverPath(f.ctx, f.member, "00000000000000000000000000000000", music.CoverSmall); !errors.Is(err, music.ErrNotFound) {
		t.Errorf("unknown cover: %v", err)
	}
}

func TestRescanFollowsRenamesAndRemovalsAndKeepsUserData(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.shared, "Inbox", "song.mp3"), mp3File(5, id3Frame("TIT2", text("Song"))))
	write(t, filepath.Join(f.shared, "gone.mp3"), mp3File(5, id3Frame("TIT2", text("Gone"))))
	f.scan(t, f.admin)
	song := f.tracks(t, f.admin)["Song"]
	if err := f.library.SetFavorite(f.ctx, f.admin, song.ID, true); err != nil {
		t.Fatal(err)
	}
	playlist, err := f.library.CreatePlaylist(f.ctx, f.admin, "  Road trip ", []string{song.ID})
	if err != nil || playlist.Name != "Road trip" {
		t.Fatalf("create playlist = %+v, %v", playlist, err)
	}

	if err := os.MkdirAll(filepath.Join(f.shared, "Albums"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.shared, "Inbox", "song.mp3"), filepath.Join(f.shared, "Albums", "renamed.mp3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.shared, "gone.mp3")); err != nil {
		t.Fatal(err)
	}
	status := f.scan(t, f.admin)
	tracks := f.tracks(t, f.admin)
	moved := tracks["Song"]
	if status.Tracks != 1 || moved.ID != song.ID || moved.Folder != "Albums" || moved.Name != "renamed.mp3" {
		t.Fatalf("after rename: %d tracks, %+v", status.Tracks, moved)
	}
	favorites, err := f.library.Favorites(f.ctx, f.admin)
	if err != nil || len(favorites) != 1 || favorites[0].TrackID != song.ID {
		t.Errorf("favorites = %+v, %v", favorites, err)
	}
	if err := f.library.RecordPlay(f.ctx, f.admin, song.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.library.RecordPlay(f.ctx, f.admin, song.ID); err != nil {
		t.Fatal(err)
	}
	recent, err := f.library.Recent(f.ctx, f.admin)
	if err != nil || len(recent) != 1 || recent[0].Plays != 2 {
		t.Errorf("recent = %+v, %v", recent, err)
	}
}

func TestMusicFoldersNarrowTheLibrary(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.shared, "Music", "a.mp3"), mp3File(5, id3Frame("TIT2", text("In music"))))
	write(t, filepath.Join(f.shared, "Music Videos", "b.mp3"), mp3File(5, id3Frame("TIT2", text("Beside music"))))
	write(t, filepath.Join(f.shared, "Audiobooks", "c.mp3"), mp3File(5, id3Frame("TIT2", text("Audiobook"))))
	f.scan(t, f.admin)
	if tracks := f.tracks(t, f.admin); len(tracks) != 3 {
		t.Fatalf("default folders hold %d tracks, want 3", len(tracks))
	}
	entries, err := f.files.Tree(f.ctx, f.admin, f.spaceIDs["shared"], "")
	if err != nil {
		t.Fatal(err)
	}
	var musicFolder string
	for _, entry := range entries {
		if entry.Path == "Music" {
			musicFolder = entry.ID
		}
	}
	sources, err := f.library.SetSources(f.ctx, f.admin, []music.SourceInput{{SpaceID: f.spaceIDs["shared"], FolderID: musicFolder}}, false)
	if err != nil || !sources.Custom || len(sources.Items) != 1 || sources.Items[0].Path != "Music" {
		t.Fatalf("set sources = %+v, %v", sources, err)
	}
	tracks := f.tracks(t, f.admin)
	if _, ok := tracks["In music"]; !ok || len(tracks) != 1 {
		t.Errorf("custom folder shows %v, want only the Music folder", tracks)
	}
	if _, err := f.library.SetSources(f.ctx, f.admin, []music.SourceInput{{SpaceID: "space:unknown"}}, false); !errors.Is(err, music.ErrNotFound) {
		t.Errorf("unknown space accepted: %v", err)
	}
	if sources, err := f.library.SetSources(f.ctx, f.admin, nil, true); err != nil || sources.Custom || len(sources.Items) != 2 {
		t.Errorf("default sources = %+v, %v", sources, err)
	}
}

func TestPlaylistsBelongToTheirMember(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.shared, "a.mp3"), mp3File(5, id3Frame("TIT2", text("A"))))
	write(t, filepath.Join(f.shared, "b.mp3"), mp3File(5, id3Frame("TIT2", text("B"))))
	f.scan(t, f.admin)
	tracks := f.tracks(t, f.admin)
	a, b := tracks["A"].ID, tracks["B"].ID
	playlist, err := f.library.CreatePlaylist(f.ctx, f.admin, "Mix", nil)
	if err != nil {
		t.Fatal(err)
	}
	if playlist, err = f.library.AddToPlaylist(f.ctx, f.admin, playlist.ID, []string{a, b, a}); err != nil || len(playlist.TrackIDs) != 3 {
		t.Fatalf("add = %+v, %v", playlist, err)
	}
	name, order := "Evening", []string{b, a}
	if playlist, err = f.library.UpdatePlaylist(f.ctx, f.admin, playlist.ID, music.PlaylistChange{Name: &name, TrackIDs: &order}); err != nil || playlist.Name != "Evening" || playlist.TrackIDs[0] != b {
		t.Fatalf("update = %+v, %v", playlist, err)
	}
	if _, err := f.library.UpdatePlaylist(f.ctx, f.member, playlist.ID, music.PlaylistChange{Name: &name}); !errors.Is(err, music.ErrNotFound) {
		t.Errorf("member changed another member's playlist: %v", err)
	}
	if lists, _ := f.library.Playlists(f.ctx, f.member); len(lists) != 0 {
		t.Errorf("member sees %d playlists", len(lists))
	}
	blank := " "
	if _, err := f.library.UpdatePlaylist(f.ctx, f.admin, playlist.ID, music.PlaylistChange{Name: &blank}); !errors.Is(err, music.ErrInvalid) {
		t.Errorf("blank name accepted: %v", err)
	}
	if err := f.library.DeletePlaylist(f.ctx, f.admin, playlist.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.library.DeletePlaylist(f.ctx, f.admin, playlist.ID); !errors.Is(err, music.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

// fakeWorker answers like the media worker: a probe from the test and FLAC
// that records how it was asked for.
type fakeWorker struct {
	mu          sync.Mutex
	probe       ffmpegworker.Probe
	cover       []byte
	unavailable bool
	// coverless makes rendering a cover find no worker.
	coverless bool
	requests  []ffmpegworker.AudioRequest
}

func (w *fakeWorker) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	if w.unavailable {
		return ffmpegworker.Probe{}, ffmpegworker.ErrUnavailable
	}
	return w.probe, nil
}
func (w *fakeWorker) Frame(context.Context, *os.File, float64, int) ([]byte, error) {
	if w.coverless {
		return nil, ffmpegworker.ErrUnavailable
	}
	return w.cover, nil
}
func (w *fakeWorker) Image(context.Context, *os.File, int) ([]byte, error) {
	return nil, errors.New("not used by music")
}
func (w *fakeWorker) Subtitle(context.Context, *os.File, ffmpegworker.SubtitleRequest) ([]byte, error) {
	return nil, errors.New("not used by music")
}
func (w *fakeWorker) Stream(context.Context, *os.File, ffmpegworker.StreamRequest) (io.ReadCloser, error) {
	return nil, errors.New("not used by music")
}
func (w *fakeWorker) Audio(_ context.Context, _ *os.File, request ffmpegworker.AudioRequest) (io.ReadCloser, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.requests = append(w.requests, request)
	return io.NopCloser(strings.NewReader("fLaC decoded")), nil
}

func TestFormatsBrowsersCannotPlayAreProbedAndDecoded(t *testing.T) {
	worker := &fakeWorker{cover: pngImage(t, 90), probe: ffmpegworker.Probe{
		Format: ffmpegworker.ProbeFormat{Name: "ape", Duration: 245.5, Tags: map[string]string{
			"Title": "无损的歌", "Artist": "林间", "Album Artist": "林间", "Album": "山野", "Track": "2/9", "Year": "2020", "Genre": "民谣",
		}},
		Streams: []ffmpegworker.ProbeStream{
			{Index: 0, Type: "audio", Codec: "ape", SampleRate: 96000, SampleFormat: "s32p", Bits: 24},
			{Index: 1, Type: "video", Codec: "mjpeg", AttachedPic: true},
		},
	}}
	f := newFixtureWith(t, worker)
	write(t, filepath.Join(f.shared, "Hi-Res", "02.ape"), []byte("MAC lossless audio"))
	write(t, filepath.Join(f.shared, "plain.mp3"), mp3File(5, id3Frame("TIT2", text("Plain"))))
	f.scan(t, f.admin)
	tracks := f.tracks(t, f.admin)
	song, ok := tracks["无损的歌"]
	if !ok {
		t.Fatalf("probed tags were not used: %v", tracks)
	}
	if song.Artist != "林间" || song.AlbumArtist != "林间" || song.Album != "山野" || song.TrackNo != 2 || song.Year != 2020 ||
		song.DurationMs != 245500 || song.Format != "ape" || !song.Transcode || song.CoverID == "" || song.ContentType() != "audio/flac" {
		t.Fatalf("decoded track = %+v", song)
	}
	if plain := tracks["Plain"]; plain.Transcode {
		t.Errorf("an MP3 is marked for decoding: %+v", plain)
	}

	body, err := f.library.Decode(f.ctx, f.admin, song.ID, 30)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	data, _ := io.ReadAll(body)
	_ = body.Close()
	if string(data) != "fLaC decoded" || len(worker.requests) != 1 || worker.requests[0] != (ffmpegworker.AudioRequest{Start: 30, Bits: 24}) {
		t.Fatalf("decoded %q with %+v", data, worker.requests)
	}
	if _, err := f.library.Decode(f.ctx, f.admin, song.ID, 400); !errors.Is(err, music.ErrInvalid) {
		t.Errorf("a start past the end = %v", err)
	}
	if _, err := f.library.Decode(f.ctx, f.admin, tracks["Plain"].ID, 0); !errors.Is(err, music.ErrInvalid) {
		t.Errorf("decoding a track browsers play = %v", err)
	}
	if _, err := f.library.Decode(f.ctx, f.member, "file:unknown", 0); !errors.Is(err, music.ErrNotFound) {
		t.Errorf("decoding an unknown track = %v", err)
	}
}

func TestWithoutAWorkerDecodedFormatsKeepTheirNames(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.shared, "旧时光合唱团 - 老街.wma"), []byte("ASF"))
	f.scan(t, f.admin)
	street, ok := f.tracks(t, f.admin)["老街"]
	if !ok || street.Artist != "旧时光合唱团" || !street.Transcode || street.DurationMs != 0 {
		t.Fatalf("track without a worker = %+v (%v)", street, ok)
	}
	if _, err := f.library.Decode(f.ctx, f.admin, street.ID, 0); !errors.Is(err, music.ErrDecoderUnavailable) {
		t.Errorf("Decode() without a worker = %v", err)
	}

	// A worker that is down for a moment leaves the file for the next scan
	// rather than indexing it without its tags.
	worker := &fakeWorker{unavailable: true}
	g := newFixtureWith(t, worker)
	write(t, filepath.Join(g.shared, "song.wv"), []byte("wvpk"))
	if status := g.scan(t, g.admin); status.Tracks != 0 {
		t.Fatalf("indexed %d tracks while the worker was down", status.Tracks)
	}
	worker.unavailable = false
	worker.probe = ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Name: "wv", Duration: 3, Tags: map[string]string{"TITLE": "Back"}}}
	if _, ok := g.tracks(t, g.admin)["Back"]; ok {
		t.Fatal("track appeared before a scan")
	}
	g.scan(t, g.admin)
	if back, ok := g.tracks(t, g.admin)["Back"]; !ok || back.DurationMs != 3000 {
		t.Fatalf("after the worker returned = %+v (%v)", back, ok)
	}
}

func TestLengthsAndCoversWaitForAWorkerThatIsAway(t *testing.T) {
	// Opus has no Go reader for its length: the worker measures it once the
	// tags are read, and a worker that is away leaves it for the next scan
	// instead of recording it as unknown for good.
	worker := &fakeWorker{unavailable: true}
	f := newFixtureWith(t, worker)
	write(t, filepath.Join(f.shared, "晚风.opus"), []byte("OggS opus"))
	f.scan(t, f.admin)
	if song, ok := f.tracks(t, f.admin)["晚风"]; !ok || song.DurationMs != 0 {
		t.Fatalf("track while the worker was away = %+v (%v)", song, ok)
	}
	worker.unavailable = false
	worker.probe = ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Name: "ogg", Duration: 61.5}}
	f.scan(t, f.admin)
	if song := f.tracks(t, f.admin)["晚风"]; song.DurationMs != 61500 {
		t.Fatalf("length after the worker returned = %d ms, want 61500", song.DurationMs)
	}

	// An embedded cover only the worker renders waits with its file.
	worker.coverless, worker.cover = true, pngImage(t, 90)
	worker.probe = ffmpegworker.Probe{
		Format:  ffmpegworker.ProbeFormat{Name: "ape", Duration: 3, Tags: map[string]string{"Title": "封面"}},
		Streams: []ffmpegworker.ProbeStream{{Index: 0, Type: "audio", Codec: "ape"}, {Index: 1, Type: "video", Codec: "mjpeg", AttachedPic: true}},
	}
	write(t, filepath.Join(f.shared, "cover.ape"), []byte("MAC"))
	f.scan(t, f.admin)
	if song, ok := f.tracks(t, f.admin)["封面"]; ok {
		t.Fatalf("indexed without the cover the worker could not render: %+v", song)
	}
	worker.coverless = false
	f.scan(t, f.admin)
	if song, ok := f.tracks(t, f.admin)["封面"]; !ok || song.CoverID == "" {
		t.Fatalf("track after the worker returned = %+v (%v)", song, ok)
	}
}

// TestRealFFmpegDecodesThroughTheWorker indexes and plays WavPack and AIFF
// files with FFmpeg when it is installed.
func TestRealFFmpegDecodesThroughTheWorker(t *testing.T) {
	tools, ok := ffmpegworker.LocalTools()
	if !ok {
		t.Skip("FFmpeg is not installed")
	}
	f := newFixtureWith(t, ffmpegworker.Local{Tools: tools})
	for _, sample := range []struct{ name, codec, title string }{{"wavpack.wv", "wavpack", "WavPack 歌曲"}, {"aiff.aiff", "pcm_s24be", "AIFF 歌曲"}} {
		path := filepath.Join(f.shared, "Lossless", sample.name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		generate := exec.Command(tools.FFmpeg, "-hide_banner", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=330:duration=4:sample_rate=96000",
			"-ac", "2", "-c:a", sample.codec, "-metadata", "title="+sample.title, "-metadata", "artist=北岸", "-metadata", "album=北方以北",
			"-metadata", "track=3", path)
		if sample.codec == "pcm_s24be" {
			// AIFF files carry their tags in an ID3 chunk, as iTunes writes them.
			generate.Args = append(generate.Args[:len(generate.Args)-1], "-write_id3v2", "1", path)
		}
		if output, err := generate.CombinedOutput(); err != nil {
			t.Skipf("FFmpeg cannot generate %s: %v %s", sample.name, err, output)
		}
	}
	f.scan(t, f.admin)
	tracks := f.tracks(t, f.admin)
	for _, title := range []string{"WavPack 歌曲", "AIFF 歌曲"} {
		track, ok := tracks[title]
		if !ok || track.Artist != "北岸" || track.Album != "北方以北" || track.DurationMs < 3900 || !track.Transcode {
			t.Fatalf("%s = %+v (%v); library %v", title, track, ok, tracks)
		}
		body, err := f.library.Decode(f.ctx, f.admin, track.ID, 1)
		if err != nil {
			t.Fatalf("%s: Decode() error = %v", title, err)
		}
		data, _ := io.ReadAll(body)
		_ = body.Close()
		if !bytes.HasPrefix(data, []byte("fLaC")) || len(data) < 10_000 {
			t.Fatalf("%s: decoded %d bytes", title, len(data))
		}
	}
}
