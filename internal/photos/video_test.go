package photos_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// videoDecoder reads every file as the video probe describes and renders
// any frame as a small JPEG; streams and frames are recorded.
type videoDecoder struct {
	probe   mediaworker.Probe
	mu      sync.Mutex
	frames  []float64
	streams []mediaworker.StreamRequest
}

func (v *videoDecoder) Probe(context.Context, *os.File) (mediaworker.Probe, error) {
	return v.probe, nil
}
func (v *videoDecoder) Image(context.Context, *os.File, int) ([]byte, error) {
	return nil, mediaworker.ErrUnreadable
}
func (v *videoDecoder) Frame(_ context.Context, _ *os.File, at float64, _ int) ([]byte, error) {
	v.mu.Lock()
	v.frames = append(v.frames, at)
	v.mu.Unlock()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 32, 18)), nil); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
func (v *videoDecoder) Stream(_ context.Context, _ *os.File, request mediaworker.StreamRequest) (io.ReadCloser, error) {
	v.mu.Lock()
	v.streams = append(v.streams, request)
	v.mu.Unlock()
	return io.NopCloser(bytes.NewReader([]byte("fragmented mp4"))), nil
}

// phoneVideo is an iPhone video held upright: stored landscape and turned
// by its display matrix, recorded at 18:20:30 in UTC+8.
func phoneVideo() mediaworker.Probe {
	return mediaworker.Probe{
		Format: mediaworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 42.5,
			Tags: map[string]string{"com.apple.quicktime.creationdate": "2024-05-01T18:20:30+0800", "creation_time": "2024-05-01T10:20:31.000000Z"}},
		Streams: []mediaworker.ProbeStream{
			{Type: "video", Codec: "hevc", PixelFormat: "yuv420p", Width: 1920, Height: 1080, Rotation: -90},
			{Type: "audio", Codec: "aac"},
		},
	}
}

// mp4Bytes is the start of an MP4 file; the decoder never reads further.
func mp4Bytes(seed byte) []byte {
	b := make([]byte, 600)
	binary.BigEndian.PutUint32(b, 24)
	copy(b[4:], "ftypisom")
	b[len(b)-1] = seed
	return b
}

func videoService(t *testing.T, probe mediaworker.Probe) (*photos.Service, *videoDecoder, string) {
	t.Helper()
	decoder := &videoDecoder{probe: probe}
	root := filepath.Join(t.TempDir(), "photos")
	s, err := photos.Open(root, photos.Options{Media: decoder, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, decoder, root
}

func catalog(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAVideoIsAPhotoAssetThatPlaysAndHasAPoster(t *testing.T) {
	ctx := context.Background()
	s, decoder, root := videoService(t, phoneVideo())
	private, shared := libraries(t, s, alice)
	asset, err := s.Import(ctx, alice, photos.ImportRequest{LibraryID: private.ID, Name: "IMG_0042.MOV", Content: bytes.NewReader(mp4Bytes(1))})
	if err != nil {
		t.Fatalf("Import(video) error = %v", err)
	}
	if asset.Video == nil || asset.Video.Duration != 42.5 || asset.Motion != nil || asset.MediaType != "video/mp4" {
		t.Fatalf("video asset = %+v", asset)
	}
	if asset.Width != 1080 || asset.Height != 1920 {
		t.Fatalf("size %dx%d; want it upright, as the display matrix turns it", asset.Width, asset.Height)
	}
	if asset.TakenAt == nil || !asset.TakenAt.Equal(time.Date(2024, 5, 1, 10, 20, 30, 0, time.UTC)) {
		t.Fatalf("taken at %v; want Apple's creation date with its offset", asset.TakenAt)
	}
	if asset.Thumbnail != photos.ThumbnailPending {
		t.Fatalf("thumbnail = %s before the media job", asset.Thumbnail)
	}
	if processed, err := s.ProcessMediaJob(ctx); err != nil || !processed {
		t.Fatalf("ProcessMediaJob() = %v, %v", processed, err)
	}
	if len(decoder.frames) != 1 || decoder.frames[0] != 1 {
		t.Fatalf("frames %v; want one a second in", decoder.frames)
	}
	thumbnail, err := s.Thumbnail(ctx, alice, asset.ID)
	if err != nil {
		t.Fatalf("Thumbnail() error = %v", err)
	}
	_ = thumbnail.Reader.Close()
	poster, err := s.Preview(ctx, alice, asset.ID)
	if err != nil || poster.MediaType != "image/jpeg" {
		t.Fatalf("Preview() = %+v, %v; want the poster frame", poster, err)
	}
	_ = poster.Reader.Close()

	// The browser reads the original as MP4, or a converted stream.
	original, err := s.OpenVideo(ctx, alice, asset.ID)
	if err != nil || original.MediaType != "video/mp4" || original.SizeBytes != 600 {
		t.Fatalf("OpenVideo() = %+v, %v", original, err)
	}
	_ = original.Reader.Close()
	for caps, want := range map[photos.BrowserCaps]string{{HEVC: true}: photos.PlayDirect, {}: photos.PlayTranscode} {
		playback, err := s.VideoPlayback(ctx, alice, asset.ID, caps)
		if err != nil || playback.Mode != want || playback.Duration != 42.5 {
			t.Fatalf("VideoPlayback(%+v) = %+v, %v; want %s", caps, playback, err, want)
		}
	}
	stream, err := s.StreamVideo(ctx, alice, asset.ID, photos.VideoStreamRequest{Start: 12})
	if err != nil {
		t.Fatalf("StreamVideo() error = %v", err)
	}
	stream.Close()
	if got := decoder.streams[0]; got.Start != 12 || got.CopyVideo || got.Height != 1080 || got.AudioTrack != 0 {
		t.Fatalf("stream request %+v; want HEVC transcoded from 12 s for a browser without it", got)
	}
	if _, err := s.StreamVideo(ctx, alice, asset.ID, photos.VideoStreamRequest{Start: 42.5}); !errors.Is(err, photos.ErrInvalidStart) {
		t.Fatalf("stream past the end error = %v", err)
	}
	if _, err := s.StreamVideo(ctx, bob, asset.ID, photos.VideoStreamRequest{}); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("another member streamed a private video: %v", err)
	}

	// It is a photo asset in every other way.
	copied, err := s.Copy(ctx, alice, asset.ID, shared.ID, "")
	if err != nil || copied.Video == nil || copied.Thumbnail != photos.ThumbnailReady {
		t.Fatalf("Copy() = %+v, %v", copied, err)
	}
	album, err := s.CreateAlbum(ctx, alice, private.ID, "生日")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddToAlbum(ctx, alice, album.ID, asset.ID); err != nil {
		t.Fatalf("AddToAlbum(video) error = %v", err)
	}
	for _, id := range []string{asset.ID, copied.ID} {
		if _, err := s.Trash(ctx, alice, id); err != nil {
			t.Fatal(err)
		}
		if err := s.Purge(ctx, alice, id); err != nil {
			t.Fatal(err)
		}
	}
	db := catalog(t, root)
	var objects, videos, derived int
	if err := db.QueryRow("SELECT (SELECT COUNT(*) FROM objects), (SELECT COUNT(*) FROM videos), (SELECT COUNT(*) FROM derived_files)").Scan(&objects, &videos, &derived); err != nil {
		t.Fatal(err)
	}
	if objects != 0 || videos != 0 || derived != 0 || len(objectFiles(t, root)) != 0 {
		t.Fatalf("purged video left objects %d, videos %d, derived %d", objects, videos, derived)
	}
}

func TestVideosTakeNoPartInLocalAI(t *testing.T) {
	ctx := context.Background()
	s, _, root := videoService(t, phoneVideo())
	private, _ := libraries(t, s, alice)
	video := importPhoto(t, s, alice, private.ID, "", "生日蛋糕.mp4", mp4Bytes(2))
	photo := importPhoto(t, s, alice, private.ID, "", "cake.png", pngBytes(t, 9))
	for {
		processed, err := s.ProcessMediaJob(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			break
		}
	}
	// Turning people on again queues every photo with a thumbnail.
	if _, err := s.SetPeople(ctx, alice, private.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPeople(ctx, alice, private.ID, true); err != nil {
		t.Fatal(err)
	}
	db := catalog(t, root)
	rows, err := db.Query("SELECT j.derivation, o.media_type FROM jobs j JOIN objects o ON o.id = j.object_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	queued := 0
	for rows.Next() {
		var derivation, mediaType string
		if err := rows.Scan(&derivation, &mediaType); err != nil {
			t.Fatal(err)
		}
		if mediaType != "image/png" {
			t.Fatalf("%s queued for a %s", derivation, mediaType)
		}
		queued++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if queued == 0 {
		t.Fatal("the photo was not queued for AI; the test proves nothing")
	}
	status, err := s.AIStatus(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if status.Pending+status.Ready+status.Failed != 1 {
		t.Fatalf("AI status %+v counts the video", status)
	}
	if _, err := s.Similar(ctx, alice, video.ID, photos.SimilarRequest{}); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Similar(video) error = %v", err)
	}
	// Search finds a video by its name, and only so.
	page, err := s.Search(ctx, alice, photos.SearchRequest{Query: "生日"})
	if err != nil || len(page.Assets) != 1 || page.Assets[0].ID != video.ID || page.Closest != 1 {
		t.Fatalf("Search(生日) = %+v, %v", page, err)
	}
	if page, err := s.Search(ctx, alice, photos.SearchRequest{Query: "cake"}); err != nil || len(page.Assets) != 1 || page.Assets[0].ID != photo.ID {
		t.Fatalf("Search(cake) = %+v, %v", page, err)
	}
}

func TestTheAssistantDoesNotSeeVideos(t *testing.T) {
	ctx := context.Background()
	s, _, _ := videoService(t, phoneVideo())
	private, _ := libraries(t, s, alice)
	video := importPhoto(t, s, alice, private.ID, "", "生日.mp4", mp4Bytes(3))
	photo := importPhoto(t, s, alice, private.ID, "", "生日.png", pngBytes(t, 3))
	album, err := s.CreateAlbum(ctx, alice, private.ID, "生日")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{video.ID, photo.ID} {
		if _, err := s.AddToAlbum(ctx, alice, album.ID, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddTag(ctx, alice, id, "生日"); err != nil {
			t.Fatal(err)
		}
	}
	assistant := photos.Principal{UserID: alice.UserID, Assistant: true}
	if _, err := s.Get(ctx, assistant, video.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("assistant Get(video) error = %v", err)
	}
	// No pixels of a video reach a model: the assistant cannot find it, and
	// a video has no view for anyone.
	if _, err := s.View(ctx, assistant, video.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("assistant View(video) error = %v", err)
	}
	if _, err := s.View(ctx, alice, video.ID); !errors.Is(err, photos.ErrNoView) {
		t.Fatalf("View(video) error = %v", err)
	}
	if _, err := s.Metadata(ctx, assistant, video.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("assistant Metadata(video) error = %v", err)
	}
	if _, err := s.Metadata(ctx, alice, video.ID); !errors.Is(err, photos.ErrNoMetadata) {
		t.Fatalf("Metadata(video) error = %v", err)
	}
	onlyPhoto := func(what string, page photos.Page, err error) {
		t.Helper()
		if err != nil || len(page.Assets) != 1 || page.Assets[0].ID != photo.ID {
			t.Fatalf("assistant %s = %+v, %v; want the photo alone", what, page, err)
		}
	}
	page, err := s.Timeline(ctx, assistant, private.ID, "", 10)
	onlyPhoto("timeline", page, err)
	page, err = s.AlbumPhotos(ctx, assistant, album.ID, "", 10)
	onlyPhoto("album", page, err)
	page, err = s.TagPhotos(ctx, assistant, "生日", "", "", 10)
	onlyPhoto("tag", page, err)
	found, err := s.Search(ctx, assistant, photos.SearchRequest{Query: "生日"})
	onlyPhoto("search", photos.Page{Assets: found.Assets}, err)
	listing, err := s.ListDirectory(ctx, assistant, private.ID, "", "", 10)
	onlyPhoto("directory", photos.Page{Assets: listing.Assets}, err)
	months, err := s.TimelineMonths(ctx, assistant, private.ID)
	if err != nil || len(months) != 1 || months[0].Photos != 1 {
		t.Fatalf("assistant months = %+v, %v", months, err)
	}
	albums, err := s.Albums(ctx, assistant, private.ID)
	if err != nil || len(albums) != 1 || albums[0].Photos != 1 {
		t.Fatalf("assistant albums = %+v, %v", albums, err)
	}
	tags, err := s.TagCounts(ctx, assistant, "")
	if err != nil || len(tags) != 1 || tags[0].Photos != 1 || tags[0].CoverID != photo.ID {
		t.Fatalf("assistant tags = %+v, %v", tags, err)
	}
	// The member sees both.
	if months, err := s.TimelineMonths(ctx, alice, private.ID); err != nil || len(months) != 2 || months[0].Videos != 0 || months[1].Videos != 1 {
		t.Fatalf("member months = %+v, %v; want the photo's and the video's", months, err)
	}
}

// Each asset rule is written once and read two ways: as SQL for listings and
// on a record for a single asset. The two must agree, or a listing would
// show what opening the asset refuses.
func TestAssetRulesReadTheSameInListingsAndOnOneAsset(t *testing.T) {
	ctx := context.Background()
	s, _, _ := videoService(t, phoneVideo())
	private, shared := libraries(t, s, alice)
	video := importPhoto(t, s, alice, private.ID, "", "clip.mp4", mp4Bytes(4))
	importPhoto(t, s, alice, private.ID, "", "still.png", pngBytes(t, 4))
	importPhoto(t, s, bob, shared.ID, "", "shared.mp4", mp4Bytes(5))
	trashed := importPhoto(t, s, alice, private.ID, "", "gone.png", pngBytes(t, 5))
	if _, err := s.Trash(ctx, alice, trashed.ID); err != nil {
		t.Fatal(err)
	}
	assistant := photos.Principal{UserID: alice.UserID, Assistant: true}
	for _, p := range []photos.Principal{alice, assistant} {
		listed, opened, err := photos.AssetRuleForms(ctx, s, p)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(listed, opened) {
			t.Fatalf("%+v: listings show %v, single assets %v", p, listed, opened)
		}
		// The forms agree on something: the assistant loses both videos.
		if want := 3 - 2*boolInt(p.Assistant); len(listed) != want || slices.Contains(listed, trashed.ID) {
			t.Fatalf("%+v sees %v, want %d untrashed assets", p, listed, want)
		}
		if slices.Contains(listed, video.ID) == p.Assistant {
			t.Fatalf("%+v sees %v; only the member sees the video", p, listed)
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Videos take no part in local AI because no AI job is ever queued for them,
// not because each AI listing happens to find no vector: one stored anyway,
// as an index that also covered videos would hold, shows up nowhere.
func TestAVideoWithAVectorStaysOutOfClustersAndSimilarPhotos(t *testing.T) {
	ctx := context.Background()
	embedder := &colorEmbedder{}
	root := filepath.Join(t.TempDir(), "photos")
	s, err := photos.Open(root, photos.Options{Now: newClock().Now, DisableCapacityReserve: true,
		Media: &videoDecoder{probe: phoneVideo()}, AI: embedder, Labels: colourLabels(t, "colour-1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	private, _ := libraries(t, s, namedAlice)
	photo := importPhoto(t, s, namedAlice, private.ID, "", "red.png", red)
	other := importPhoto(t, s, namedAlice, private.ID, "", "crimson.png", solidPNG(250, 5, 5))
	video := importPhoto(t, s, namedAlice, private.ID, "", "red.mp4", mp4Bytes(6))
	index(t, s, embedder)
	db := catalog(t, root)
	if _, err := db.Exec(`INSERT INTO embeddings(object_id, derivation, model, vector, created_at)
SELECT (SELECT object_id FROM assets WHERE id = ?), derivation, model, vector, created_at FROM embeddings
WHERE object_id = (SELECT object_id FROM assets WHERE id = ?)`, video.ID, photo.ID); err != nil {
		t.Fatal(err)
	}
	assistant := photos.Principal{UserID: namedAlice.UserID, Username: "alice", Assistant: true}
	for _, p := range []photos.Principal{namedAlice, assistant} {
		if got := cluster(t, s, p, "red"); slices.Contains(got, "red.mp4") || len(got) != 2 {
			t.Fatalf("%+v red cluster = %v, want the two photos", p, got)
		}
		counts, ready, err := s.LabelCounts(ctx, p, "")
		if err != nil || !ready || len(counts) != 1 || counts[0].Photos != 2 {
			t.Fatalf("%+v label counts = %+v, %v, %v; want red with two photos", p, counts, ready, err)
		}
		similar, err := s.Similar(ctx, p, other.ID, photos.SimilarRequest{})
		if err != nil || !similar.Ready || len(similar.Assets) != 1 || similar.Assets[0].ID != photo.ID {
			t.Fatalf("%+v similar to crimson = %+v, %v; want the red photo alone", p, similar, err)
		}
		// Search matches a video by its name only, and never for the assistant.
		found, err := s.Search(ctx, p, photos.SearchRequest{Query: "red.mp4"})
		if err != nil || slices.Contains(names(found.Assets), "red.mp4") == p.Assistant {
			t.Fatalf("%+v search for the video's name = %v, %v", p, names(found.Assets), err)
		}
		considered, err := photos.SearchCandidates(ctx, s, p, private.ID)
		if err != nil || len(considered) != 3-boolInt(p.Assistant) || slices.Contains(considered, video.ID) == p.Assistant {
			t.Fatalf("%+v search candidates = %v, %v", p, considered, err)
		}
		// Whatever an index returns, the page shows only what p may list.
		shown, err := photos.RecheckHits(ctx, s, p, video.ID, photo.ID)
		if want := map[bool]int{false: 2, true: 1}[p.Assistant]; err != nil || len(shown) != want || slices.Contains(shown, video.ID) == p.Assistant {
			t.Fatalf("%+v recheck of the video and a photo = %v, %v", p, shown, err)
		}
	}
}

func TestOnlyReadableVideosAreImported(t *testing.T) {
	cases := map[string]func(*mediaworker.Probe){
		"audio only":     func(p *mediaworker.Probe) { p.Streams = p.Streams[1:] },
		"no duration":    func(p *mediaworker.Probe) { p.Format.Duration = 0 },
		"unknown format": func(p *mediaworker.Probe) { p.Format.Name = "image2" },
		"only a cover":   func(p *mediaworker.Probe) { p.Streams[0].AttachedPic = true },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			probe := phoneVideo()
			change(&probe)
			s, _, root := videoService(t, probe)
			private, _ := libraries(t, s, alice)
			_, err := s.Import(context.Background(), alice, photos.ImportRequest{LibraryID: private.ID, Name: "x.mp4", Content: bytes.NewReader(mp4Bytes(4))})
			if !errors.Is(err, photos.ErrUnsupportedType) {
				t.Fatalf("Import() error = %v", err)
			}
			if files, _ := os.ReadDir(filepath.Join(root, "staging")); len(files) != 0 || len(objectFiles(t, root)) != 0 {
				t.Fatal("a refused video left files behind")
			}
		})
	}
	t.Run("without the media worker", func(t *testing.T) {
		s, _, _ := newService(t)
		private, _ := libraries(t, s, alice)
		_, err := s.Import(context.Background(), alice, photos.ImportRequest{LibraryID: private.ID, Name: "x.mp4", Content: bytes.NewReader(mp4Bytes(5))})
		if !errors.Is(err, mediaworker.ErrUnavailable) {
			t.Fatalf("Import() error = %v", err)
		}
	})
	t.Run("over the video limit", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "photos")
		s, err := photos.Open(root, photos.Options{Media: &videoDecoder{probe: phoneVideo()}, MaxImportBytes: 100, MaxVideoBytes: 1000, DisableCapacityReserve: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		private, _ := libraries(t, s, alice)
		if _, err := s.Import(context.Background(), alice, photos.ImportRequest{LibraryID: private.ID, Name: "small.mp4", Content: bytes.NewReader(mp4Bytes(6))}); err != nil {
			t.Fatalf("a video under its own limit but over the photo limit: %v", err)
		}
		big := append(mp4Bytes(7), make([]byte, 1000)...)
		if _, err := s.Import(context.Background(), alice, photos.ImportRequest{LibraryID: private.ID, Name: "big.mp4", Content: bytes.NewReader(big)}); !errors.Is(err, photos.ErrTooLarge) {
			t.Fatalf("Import(big) error = %v", err)
		}
	})
}

func TestALoneLivePhotoVideoCompletesItsStillOrBecomesAVideo(t *testing.T) {
	ctx := context.Background()
	probe := motionProbe()
	probe.Format.Tags = map[string]string{"com.apple.quicktime.content.identifier": liveID}
	s, _, _ := videoService(t, probe)
	private, shared := libraries(t, s, alice)
	still := importStill(t, s, alice, private.ID, "IMG_7.jpg", appleJPEG(t, liveID))

	result, err := s.Upload(ctx, alice, photos.ImportRequest{LibraryID: private.ID, Name: "IMG_7.MOV", Content: bytes.NewReader(movieBytes())})
	if err != nil || result.Asset != nil || !sameIDs(result.Completed, still.ID) || result.Completed[0].Motion == nil {
		t.Fatalf("Upload(MOV of a waiting still) = %+v, %v", result, err)
	}
	// In a library where no still waits for it, it is a video.
	result, err = s.Upload(ctx, alice, photos.ImportRequest{LibraryID: shared.ID, Name: "IMG_7.MOV", Content: bytes.NewReader(movieBytes())})
	if err != nil || result.Asset == nil || result.Asset.Video == nil || len(result.Completed) != 0 {
		t.Fatalf("Upload(MOV nobody waits for) = %+v, %v", result, err)
	}
	if result.Asset.Width != 320 || result.Asset.Height != 240 {
		t.Fatalf("the video took the size of no one: %+v", result.Asset)
	}
	// A photo is imported as it always was.
	result, err = s.Upload(ctx, alice, photos.ImportRequest{LibraryID: private.ID, Name: "a.png", Content: bytes.NewReader(pngBytes(t, 1))})
	if err != nil || result.Asset == nil || result.Asset.Video != nil {
		t.Fatalf("Upload(photo) = %+v, %v", result, err)
	}
}

func TestVideoCaptureTimeFallsBackToTheContainer(t *testing.T) {
	probe := phoneVideo()
	delete(probe.Format.Tags, "com.apple.quicktime.creationdate")
	s, _, _ := videoService(t, probe)
	private, _ := libraries(t, s, alice)
	asset := importPhoto(t, s, alice, private.ID, "", "android.mp4", mp4Bytes(8))
	if asset.TakenAt == nil || !asset.TakenAt.Equal(time.Date(2024, 5, 1, 10, 20, 31, 0, time.UTC)) {
		t.Fatalf("taken at %v; want the container's UTC creation time", asset.TakenAt)
	}
	probe.Format.Tags = map[string]string{"creation_time": "1970-01-01T00:00:00.000000Z"}
	s, _, _ = videoService(t, probe)
	private, _ = libraries(t, s, alice)
	if asset := importPhoto(t, s, alice, private.ID, "", "camera.mp4", mp4Bytes(9)); asset.TakenAt != nil {
		t.Fatalf("an unset creation time became %v", asset.TakenAt)
	}
}
