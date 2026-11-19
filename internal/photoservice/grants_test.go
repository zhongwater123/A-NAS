package photoservice_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
)

// grantSessions adds the accounts in use to the sessions the tests know.
type grantSessions struct {
	sessions
	mu     sync.Mutex
	active map[string]bool
	// unbounded counts the account lookups asked with no deadline.
	unbounded int
}

func (g *grantSessions) ActiveProfiles(ctx context.Context) ([]accounts.Profile, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		g.unbounded++
	}
	var profiles []accounts.Profile
	for _, user := range g.sessions {
		if g.active[user.UserID] {
			profiles = append(profiles, accounts.Profile{ID: user.UserID, Username: user.Username})
		}
	}
	return profiles, nil
}

func (g *grantSessions) set(userID string, active bool) {
	g.mu.Lock()
	g.active[userID] = active
	g.mu.Unlock()
}

// probedVideo reads every file as a short phone video.
type probedVideo struct{}

func (probedVideo) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{
		Format:  ffmpegworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 12},
		Streams: []ffmpegworker.ProbeStream{{Type: "video", Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080}},
	}, nil
}
func (probedVideo) Image(context.Context, *os.File, int) ([]byte, error) {
	return nil, ffmpegworker.ErrUnreadable
}
func (probedVideo) Frame(context.Context, *os.File, float64, int) ([]byte, error) {
	return nil, ffmpegworker.ErrUnreadable
}
func (probedVideo) Stream(context.Context, *os.File, ffmpegworker.StreamRequest) (io.ReadCloser, error) {
	return nil, ffmpegworker.ErrUnreadable
}

func videoBytes(seed byte) []byte {
	b := make([]byte, 600)
	binary.BigEndian.PutUint32(b, 24)
	copy(b[4:], "ftypisom")
	b[len(b)-1] = seed
	return b
}

func TestTheGrantSocketAnswersOnlyTheMontageServiceAsTheOwnerNow(t *testing.T) {
	ctx := context.Background()
	options := photos.Options{DisableCapacityReserve: true, Media: probedVideo{}}
	// Alice's video, in the store before the service opens it.
	dir, err := os.MkdirTemp("", "grants")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	known := &grantSessions{sessions: knownSessions, active: map[string]bool{"user:alice": true, "user:bob": true}}
	grantSocket := filepath.Join(dir, "grants.sock")
	r := start(t, func(config *photoservice.Config) {
		store, err := photos.Open(config.Root, options)
		if err != nil {
			t.Fatal(err)
		}
		alice := photos.Principal{UserID: "user:alice", Username: "alice"}
		libraries, err := store.Libraries(ctx, alice)
		if err != nil {
			t.Fatal(err)
		}
		for _, lib := range libraries {
			if lib.OwnerUserID == alice.UserID {
				if _, err := store.Import(ctx, alice, photos.ImportRequest{LibraryID: lib.ID, Name: "beach.mp4", Content: bytes.NewReader(videoBytes(1))}); err != nil {
					t.Fatal(err)
				}
			}
		}
		_ = store.Close()
		config.Sessions, config.Photos = known, options
		config.GrantSocketPath, config.GrantPeerUID = grantSocket, os.Getuid()
	})
	r.ready.Store(true)
	client := photosapi.GrantClient(grantSocket)
	var grants []photos.Grant
	waitFor(t, func() bool { grants, err = client.List(ctx); return err == nil })
	if len(grants) != 0 {
		t.Fatalf("List() = %+v", grants)
	}

	// The member's session registers; the asset ID is read from her library.
	videoID := aliceVideoID(t, r)
	grant, err := client.Register(ctx, "alice-token", "project:1", []string{videoID})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if _, err := client.Register(ctx, "bob-token", "project:2", []string{videoID}); photosapi.GrantErrorCode(err) != photosapi.GrantAssetsRefused {
		t.Fatalf("Bob registering Alice's video = %v", err)
	}
	if _, err := client.Register(ctx, "forged", "project:3", []string{videoID}); photosapi.GrantErrorCode(err) != photosapi.GrantUnauthorized {
		t.Fatalf("registering with no session = %v", err)
	}
	if _, err := client.Register(ctx, "viewer-token", "project:4", []string{videoID}); photosapi.GrantErrorCode(err) != photosapi.GrantAssetsRefused {
		t.Fatalf("an administrator in viewing mode registering Alice's video = %v", err)
	}

	// Later, with no session, the montage center opens it.
	file, objectID, err := client.Open(ctx, grant.ID, videoID)
	if err != nil || objectID == "" {
		t.Fatalf("Open() = %v, %v", objectID, err)
	}
	content, _ := io.ReadAll(file)
	_ = file.Close()
	if !bytes.Equal(content, videoBytes(1)) {
		t.Fatal("Open() handed over another file")
	}
	if looked, err := client.Lookup(ctx, grant.ID, []string{videoID}); err != nil || looked[0].Status != photos.GrantAvailable {
		t.Fatalf("Lookup() = %+v, %v", looked, err)
	}

	// Alice's account stops: her grant reaches nothing, and says why, so
	// that the montage center waits rather than giving the project up.
	known.set("user:alice", false)
	if _, _, err := client.Open(ctx, grant.ID, videoID); photosapi.GrantErrorCode(err) != photosapi.GrantOwnerInactive {
		t.Fatalf("Open() for a disabled owner = %v", err)
	}
	if _, err := client.Lookup(ctx, grant.ID, []string{videoID}); photosapi.GrantErrorCode(err) != photosapi.GrantOwnerInactive {
		t.Fatalf("Lookup() for a disabled owner = %v", err)
	}
	if err := client.Analyze(ctx, grant.ID, []string{videoID}, false); photosapi.GrantErrorCode(err) != photosapi.GrantOwnerInactive {
		t.Fatalf("Analyze() for a disabled owner = %v", err)
	}
	if _, _, err := client.Analysis(ctx, grant.ID, videoID); photosapi.GrantErrorCode(err) != photosapi.GrantOwnerInactive {
		t.Fatalf("Analysis() for a disabled owner = %v", err)
	}
	known.set("user:alice", true)
	if err := client.Revoke(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Open(ctx, grant.ID, videoID); photosapi.GrantErrorCode(err) != photosapi.GrantNotFound {
		t.Fatalf("Open() after revoking = %v", err)
	}
	if grants, err := client.List(ctx); err != nil || len(grants) != 0 {
		t.Fatalf("List() after revoking = %+v, %v", grants, err)
	}
	known.mu.Lock()
	defer known.mu.Unlock()
	if known.unbounded > 0 {
		t.Fatalf("%d account lookups had no deadline", known.unbounded)
	}
}

// grantedStore opens the store at root before the service does, with
// Alice's video and count grants of it, and returns the video's ID and the
// grants' IDs.
func grantedStore(t *testing.T, root string, options photos.Options, count int) (string, []string) {
	t.Helper()
	ctx := context.Background()
	store, err := photos.Open(root, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	alice := photos.Principal{UserID: "user:alice", Username: "alice"}
	libraries, err := store.Libraries(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	var video photos.Asset
	for _, lib := range libraries {
		if lib.OwnerUserID == alice.UserID {
			if video, err = store.Import(ctx, alice, photos.ImportRequest{LibraryID: lib.ID, Name: "beach.mp4", Content: bytes.NewReader(videoBytes(1))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var grants []string
	for i := range count {
		grant, err := store.RegisterGrant(ctx, photosapi.NewGrantPrincipal(alice.UserID, alice.Username), fmt.Sprintf("project:%d", i), []string{video.ID})
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant.ID)
	}
	return video.ID, grants
}

// However many grants there are, the list reaches the montage center whole,
// a page per answer within the message limit.
func TestTheGrantListComesInPages(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "grants")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	grantSocket := filepath.Join(dir, "grants.sock")
	options := photos.Options{DisableCapacityReserve: true, Media: probedVideo{}}
	var registered []string
	r := start(t, func(config *photoservice.Config) {
		_, registered = grantedStore(t, config.Root, options, 2*photosapi.GrantListPage+1)
		config.Sessions = &grantSessions{sessions: knownSessions, active: map[string]bool{"user:alice": true}}
		config.Photos = options
		config.GrantSocketPath, config.GrantPeerUID = grantSocket, os.Getuid()
	})
	r.ready.Store(true)
	var grants []photos.Grant
	waitFor(t, func() bool { grants, err = photosapi.GrantClient(grantSocket).List(ctx); return err == nil })
	listed := make([]string, 0, len(grants))
	for _, grant := range grants {
		listed = append(listed, grant.ID)
	}
	slices.Sort(registered)
	if !slices.Equal(listed, registered) {
		t.Fatalf("List() = %d grants; want the %d registered, in ID order", len(listed), len(registered))
	}
}

// blockingAccounts holds every account lookup until release closes.
type blockingAccounts struct {
	*grantSessions
	entered chan struct{}
	release chan struct{}
}

func (b blockingAccounts) ActiveProfiles(ctx context.Context) ([]accounts.Profile, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.grantSessions.ActiveProfiles(ctx)
}

// A grant request being answered when the service stops is answered from
// the open Catalog: the store closes only after it.
func TestAGrantRequestInFlightFinishesBeforeTheCatalogCloses(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "grants")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	grantSocket := filepath.Join(dir, "grants.sock")
	options := photos.Options{DisableCapacityReserve: true, Media: probedVideo{}}
	held := blockingAccounts{
		grantSessions: &grantSessions{sessions: knownSessions, active: map[string]bool{"user:alice": true}},
		entered:       make(chan struct{}, 1), release: make(chan struct{}),
	}
	var videoID string
	var grants []string
	r := start(t, func(config *photoservice.Config) {
		videoID, grants = grantedStore(t, config.Root, options, 1)
		config.Sessions, config.Photos = held, options
		config.GrantSocketPath, config.GrantPeerUID = grantSocket, os.Getuid()
	})
	r.ready.Store(true)
	client := photosapi.GrantClient(grantSocket)
	waitFor(t, func() bool { _, err := client.List(ctx); return err == nil })

	type answer struct {
		assets []photos.GrantedAsset
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		assets, err := client.Lookup(ctx, grants[0], []string{videoID})
		answered <- answer{assets, err}
	}()
	<-held.entered
	r.cancel()
	// Give the stop time to reach the store, as it did before it waited.
	time.Sleep(200 * time.Millisecond)
	close(held.release)
	got := <-answered
	if got.err != nil || len(got.assets) != 1 || got.assets[0].Status != photos.GrantAvailable {
		t.Fatalf("Lookup() while stopping = %+v, %v; want the video available", got.assets, got.err)
	}
	if err := <-r.done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
	r.done <- nil
}

func TestTheGrantSocketRefusesAnyOtherUID(t *testing.T) {
	dir, err := os.MkdirTemp("", "grants")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	grantSocket := filepath.Join(dir, "grants.sock")
	known := &grantSessions{sessions: knownSessions, active: map[string]bool{}}
	r := start(t, func(config *photoservice.Config) {
		config.Sessions = known
		// Only the montage service's UID is answered, never this test's.
		config.GrantSocketPath, config.GrantPeerUID = grantSocket, os.Getuid()+1
	})
	r.ready.Store(true)
	if info, err := os.Stat(grantSocket); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("grant socket mode = %v, %v", info, err)
	}
	if _, err := (photosapi.GrantClient(grantSocket)).List(context.Background()); err == nil || photosapi.GrantErrorCode(err) != "" {
		t.Fatalf("List() from another UID = %v; want the connection closed unanswered", err)
	}
}

// aliceVideoID reads the ID of the one video in Alice's private library
// through the photo API, as she would.
func aliceVideoID(t *testing.T, r running) string {
	t.Helper()
	var list struct {
		Items []photos.Library `json:"items"`
	}
	waitFor(t, func() bool {
		response := r.get(t, "/api/v1/photos/libraries", "alice-token")
		defer func() { _ = response.Body.Close() }()
		return response.StatusCode == http.StatusOK && json.NewDecoder(response.Body).Decode(&list) == nil
	})
	for _, lib := range list.Items {
		if lib.OwnerUserID != "user:alice" {
			continue
		}
		var page struct {
			Items []photos.Asset `json:"items"`
		}
		response := r.get(t, "/api/v1/photos/libraries/"+lib.ID+"/timeline", "alice-token")
		defer func() { _ = response.Body.Close() }()
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil || len(page.Items) != 1 {
			t.Fatalf("timeline = %+v, %v", page, err)
		}
		return page.Items[0].ID
	}
	t.Fatal("Alice has no private library")
	return ""
}

// threeHourVideo reads every file as a three-hour phone video.
type threeHourVideo struct{ probedVideo }

func (threeHourVideo) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{
		Format:  ffmpegworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 3 * 3600},
		Streams: []ffmpegworker.ProbeStream{{Type: "video", Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080}},
	}, nil
}

// seedWindows stores a done window analysis of the one video in the Catalog
// at root, as the AI runner would: count windows of 2 seconds with vectors
// of dimensions, every value its window's index.
func seedWindows(t *testing.T, root string, count, dimensions int) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var object string
	if err := db.QueryRow("SELECT object_id FROM assets").Scan(&object); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	vector := make([]byte, 4*dimensions)
	for i := range count {
		for d := range dimensions {
			binary.LittleEndian.PutUint32(vector[4*d:], math.Float32bits(float32(i)))
		}
		if _, err := tx.Exec(`INSERT INTO video_windows(object_id, idx, start_ms, end_ms, flat, vector, identity, created_at)
VALUES(?, ?, ?, ?, ?, ?, 'model|win', '2026-11-18T00:00:00Z')`, object, i, 2000*i, 2000*(i+1), i%2 == 0, vector); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// A long video's analysis reaches the montage center whole: its windows
// come beside the answer, which stays within the message limit.
func TestALongVideosAnalysisComesWhole(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "grants")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	grantSocket := filepath.Join(dir, "grants.sock")
	options := photos.Options{DisableCapacityReserve: true, Media: threeHourVideo{}}
	const windows, dimensions = 5400, 768
	var videoID string
	var grants []string
	r := start(t, func(config *photoservice.Config) {
		videoID, grants = grantedStore(t, config.Root, options, 1)
		seedWindows(t, config.Root, windows, dimensions)
		config.Sessions = &grantSessions{sessions: knownSessions, active: map[string]bool{"user:alice": true}}
		config.Photos = options
		config.GrantSocketPath, config.GrantPeerUID = grantSocket, os.Getuid()
	})
	r.ready.Store(true)
	client := photosapi.GrantClient(grantSocket)
	var analysis photos.GrantAnalysis
	var vectors []byte
	waitFor(t, func() bool { analysis, vectors, err = client.Analysis(ctx, grants[0], videoID); return err == nil })
	if analysis.Windows != photos.AnalysisDone || analysis.Dimensions != dimensions || len(analysis.List) != windows ||
		len(vectors) != windows*dimensions*4 {
		t.Fatalf("Analysis() = %s, %d windows of %d dimensions, %d bytes", analysis.Windows, len(analysis.List), analysis.Dimensions, len(vectors))
	}
	last := analysis.List[windows-1]
	if last != (photos.Window{Index: windows - 1, StartMs: 2000 * (windows - 1), EndMs: 2000 * windows}) || !analysis.List[0].Flat {
		t.Fatalf("windows = first %+v, last %+v", analysis.List[0], last)
	}
	if value := math.Float32frombits(binary.LittleEndian.Uint32(vectors[len(vectors)-4:])); value != windows-1 {
		t.Fatalf("the last window's vector holds %v", value)
	}
}
