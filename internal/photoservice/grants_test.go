package photoservice_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

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
}

func (g *grantSessions) ActiveProfiles(context.Context) ([]accounts.Profile, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
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

	// Alice's account stops: her grant reaches nothing.
	known.set("user:alice", false)
	if _, _, err := client.Open(ctx, grant.ID, videoID); photosapi.GrantErrorCode(err) != photosapi.GrantNotFound {
		t.Fatalf("Open() for a disabled owner = %v", err)
	}
	if _, err := client.Lookup(ctx, grant.ID, []string{videoID}); photosapi.GrantErrorCode(err) != photosapi.GrantNotFound {
		t.Fatalf("Lookup() for a disabled owner = %v", err)
	}
	known.set("user:alice", true)
	if err := client.Revoke(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Open(ctx, grant.ID, videoID); photosapi.GrantErrorCode(err) != photosapi.GrantNotFound {
		t.Fatalf("Open() after revoking = %v", err)
	}
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
