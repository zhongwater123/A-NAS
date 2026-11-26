package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/protocol"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// songMedia stands in for the media worker as the montage center uses it:
// every file is two minutes of audio.
type songMedia struct{}

func (songMedia) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Name: "mp3", Duration: 120},
		Streams: []ffmpegworker.ProbeStream{{Type: "audio", Codec: "mp3"}}}, nil
}

func (songMedia) Audio(_ context.Context, _ *os.File, request ffmpegworker.AudioRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(make([]byte, 4*request.Channels*16))), nil
}

// songStructure stands in for allin1, and songExcerpt for the compute
// Worker's music operation.
type songStructure struct{}

func (songStructure) Analyze(_ context.Context, stereo, mono *os.File, whole <-chan error) (app.Analysis, error) {
	_, _ = io.Copy(io.Discard, stereo)
	_, _ = io.Copy(io.Discard, mono)
	if err := <-whole; err != nil {
		return app.Analysis{}, err
	}
	var downbeats []float64
	for t := 0.0; t < 120; t += 2 {
		downbeats = append(downbeats, t)
	}
	return app.Analysis{Structure: videocutcompute.Structure{BPM: 120, Beats: downbeats, Downbeats: downbeats,
		Segments: []videocutcompute.Section{{Start: 0, End: 30, Label: "intro"}, {Start: 30, End: 90, Label: "chorus"}, {Start: 90, End: 120, Label: "outro"}}},
		Loudness: []float32{0.2, 0.3}}, nil
}

type songExcerpt struct{}

func (songExcerpt) Music(_ context.Context, request videocutcompute.MusicRequest) (videocutcompute.Selection, error) {
	start := 30.0
	if request.Start != nil {
		start = *request.Start
	}
	end := min(start+request.Target, 120)
	return videocutcompute.Selection{Tempo: 120, Bar: 2, Excerpt: [2]float64{start, end}, Seconds: end - start, Chorus: true,
		Parts: []videocutcompute.Part{{Start: 0, End: end - start, Level: "高潮", Label: "chorus", Slots: [][2]float64{{0, end - start}}}}}, nil
}

// TestASongComesFromTheMembersMusicLibrary chooses a song for a project
// through the Product Service as development runs it: the track is opened as
// the member and copied into the montage center, which analyses it.
func TestASongComesFromTheMembersMusicLibrary(t *testing.T) {
	dir := t.TempDir()
	controlDB := filepath.Join(dir, "control.db")
	store, err := accounts.OpenSQLite(controlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	users := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fileStore, err := files.OpenSQLite(filepath.Join(dir, "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	volume := filepath.Join(dir, "volume")
	fileService := files.NewLocalService(fileStore, volume, users, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	musicStore, err := music.OpenSQLite(filepath.Join(dir, "music.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = musicStore.Close() })
	library := music.New(musicStore, filepath.Join(dir, "covers"), fileService, users, music.Options{Worker: decodingWorker{}})
	t.Cleanup(library.Close)
	photoService, err := photos.Open(filepath.Join(dir, "photos"), photos.Options{DisableCapacityReserve: true, Media: shortVideos{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = photoService.Close() })
	if err := os.Mkdir(filepath.Join(dir, "montage"), 0o700); err != nil {
		t.Fatal(err)
	}
	montageStore, err := app.Open(t.Context(), filepath.Join(dir, "montage"), app.Options{
		Grants: photoservice.LocalGrants(photoService, sessionDirectory(t, controlDB), nil, logger),
		Media:  songMedia{}, Music: songStructure{}, Compute: songExcerpt{}, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = montageStore.Close() })
	// What reaches the montage center: the header only the Product Service sets.
	var seen []string
	api := protocol.New(montageStore, logger)
	recorded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.Header.Get(protocol.SongHeader))
		api.ServeHTTP(w, r)
	})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users, Files: fileService,
		Music: library, Montage: serviceproxy.Local(recorded), Logger: logger,
	})
	f := &activityFixture{t: t, handler: handler, client: &contractClient{t: t, handler: handler}}
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	owner.do(http.MethodPost, "/api/v1/users", `{"username":"bob","password":"bob password for testing"}`, nil, http.StatusCreated)
	bob := f.signIn("/api/v1/session", "bob", "bob password for testing", "172.18.45.21", http.StatusOK)

	// The owner's song in their own music folder, Bob's in his.
	for _, song := range []string{filepath.Join(volume, "spaces", "private", "owner", "Music", "Symphony.mp3"),
		filepath.Join(volume, "spaces", "private", "bob", "Music", "Fire.mp3")} {
		if err := os.MkdirAll(filepath.Dir(song), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(song, silentMP3(strings.TrimSuffix(filepath.Base(song), ".mp3"), 4600), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	trackOf := func(client *contractClient) string {
		client.do(http.MethodPost, "/api/v1/music/library/scan", "", nil, http.StatusAccepted)
		deadline := time.Now().Add(10 * time.Second)
		for {
			var tracks music.Library
			decode(t, client.do(http.MethodGet, "/api/v1/music/tracks", "", nil, http.StatusOK).Body, &tracks)
			if len(tracks.Items) == 1 {
				return tracks.Items[0].ID
			}
			if time.Now().After(deadline) {
				t.Fatalf("tracks = %+v", tracks)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	mine, bobs := trackOf(owner), trackOf(bob)

	var me struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	decode(t, owner.do(http.MethodGet, "/api/v1/session", "", nil, http.StatusOK).Body, &me)
	principal := photos.Principal{UserID: me.User.ID, Username: me.User.Username}
	libraries, err := photoService.Libraries(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	var video string
	for _, lib := range libraries {
		if lib.OwnerUserID == principal.UserID {
			asset, err := photoService.Import(t.Context(), principal, photos.ImportRequest{LibraryID: lib.ID, Name: "clip.mp4", Content: bytes.NewReader(shortVideo(1))})
			if err != nil {
				t.Fatal(err)
			}
			video = asset.ID
		}
	}
	var project app.ProjectDetail
	decode(t, owner.do(http.MethodPost, "/api/v1/montage/projects", `{"requestKey":"k1","assetIds":["`+video+`"]}`, nil, http.StatusCreated).Body, &project)
	path := "/api/v1/montage/projects/" + project.ID

	owner.do(http.MethodGet, path+"/song", "", nil, http.StatusNotFound)
	// Bob's track is not the owner's to copy, and answers as a missing one.
	owner.do(http.MethodPut, path+"/song", `{"trackId":"`+bobs+`"}`, nil, http.StatusNotFound)
	owner.do(http.MethodPut, path+"/song", `{"trackId":"file:missing"}`, nil, http.StatusNotFound)
	for _, request := range seen {
		if strings.HasPrefix(request, "PUT") {
			t.Fatalf("a refused track reached the montage center: %q", seen)
		}
	}
	// The browser cannot say what the song is.
	seen = nil
	var song app.SongDetail
	decode(t, owner.do(http.MethodPut, path+"/song", `{"trackId":"`+mine+`"}`, map[string]string{protocol.SongHeader: "forged"}, http.StatusOK).Body, &song)
	if song.Title != "Symphony" || song.State != app.SongAnalyzing || song.Transcode {
		t.Fatalf("song = %+v", song)
	}
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "PUT ") || seen[0] == "PUT forged" {
		t.Fatalf("the montage center saw %q", seen)
	}
	if header, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(seen[0], "PUT ")); err != nil ||
		!strings.Contains(string(header), `"title":"Symphony"`) || !strings.Contains(string(header), `"trackId":"`+mine+`"`) {
		t.Fatalf("the montage center was told %q, %v", header, err)
	}
	// Bob reaches neither the owner's project nor its song.
	bob.do(http.MethodPut, path+"/song", `{"trackId":"`+bobs+`"}`, nil, http.StatusNotFound)
	bob.do(http.MethodGet, path+"/song", "", nil, http.StatusNotFound)
	bob.do(http.MethodGet, path+"/song/audio", "", nil, http.StatusNotFound)

	// The member deletes the original; the project keeps its copy.
	if err := os.Remove(filepath.Join(volume, "spaces", "private", "owner", "Music", "Symphony.mp3")); err != nil {
		t.Fatal(err)
	}
	partial := owner.do(http.MethodGet, path+"/song/audio", "", map[string]string{"Range": "bytes=0-2"}, http.StatusPartialContent)
	if partial.Body.String() != "ID3" || partial.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("audio = %q as %q", partial.Body.String(), partial.Header().Get("Content-Type"))
	}
	owner.do(http.MethodPatch, path, `{"songStart":12}`, nil, http.StatusConflict)
	if err := montageStore.AnalyzeWaiting(t.Context()); err != nil {
		t.Fatal(err)
	}
	decode(t, owner.do(http.MethodGet, path+"/song", "", nil, http.StatusOK).Body, &song)
	if song.State != app.SongReady || song.Selection == nil || song.Selection.Excerpt != [2]float64{30, 120} {
		t.Fatalf("song = %+v", song)
	}
	owner.do(http.MethodPatch, path, `{"songStart":13.2}`, nil, http.StatusOK)
	decode(t, owner.do(http.MethodGet, path+"/song", "", nil, http.StatusOK).Body, &song)
	if song.Selection.Excerpt[0] != 14 || *song.DefaultStart != 30 {
		t.Fatalf("song after dragging = %+v", song.Selection)
	}
	// Dragged back onto the default start, it follows the default excerpt again.
	owner.do(http.MethodPatch, path, `{"songStart":30.4}`, nil, http.StatusOK)
	decode(t, owner.do(http.MethodGet, path+"/song", "", nil, http.StatusOK).Body, &song)
	if song.Selection.Excerpt != [2]float64{30, 120} {
		t.Fatalf("song back on the default start = %+v", song.Selection)
	}
	owner.do(http.MethodDelete, path, "", nil, http.StatusNoContent)
	entries, _ := os.ReadDir(filepath.Join(dir, "montage", "songs"))
	for _, entry := range entries {
		if entry.Name() != ".incoming" {
			t.Fatalf("copies after the project went: %v", entries)
		}
	}
}
