package montageapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// tracks stands in for the music library: one track, of Alice's.
type tracks struct {
	t     *testing.T
	track music.Track
}

func (l tracks) OpenTrack(_ context.Context, actor accounts.User, id string) (music.Stream, error) {
	if actor.ID != "user:alice" || id != l.track.ID {
		return music.Stream{}, music.ErrNotFound
	}
	path := filepath.Join(l.t.TempDir(), "song")
	if err := os.WriteFile(path, []byte("song"), 0o600); err != nil {
		l.t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		l.t.Fatal(err)
	}
	return music.Stream{Name: "song.flac", SizeBytes: l.track.SizeBytes, Reader: file, Track: l.track}, nil
}

func TestCopySongAnswersWhatTheLibraryKnowsWithoutCopying(t *testing.T) {
	for name, c := range map[string]struct {
		track music.Track
		user  string
		want  int
		code  string
	}{
		"another member's track": {music.Track{ID: "t"}, "user:bob", http.StatusNotFound, "not_found"},
		"larger than 100 MiB":    {music.Track{ID: "t", SizeBytes: montage.MaxSongBytes + 1}, "user:alice", http.StatusRequestEntityTooLarge, "song_too_large"},
		"shorter than 30 s":      {music.Track{ID: "t", DurationMs: 29_000}, "user:alice", http.StatusUnprocessableEntity, "song_length"},
		"longer than 8 min":      {music.Track{ID: "t", DurationMs: 480_001}, "user:alice", http.StatusUnprocessableEntity, "song_length"},
	} {
		t.Run(name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			handler := montageapi.CopySong(tracks{t: t, track: c.track}, next, nil)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/api/v1/montage/projects/p/song", strings.NewReader(`{"trackId":"t"}`))
			request.Header.Set("Content-Type", "application/json")
			handler(recorder, request, accounts.Session{User: accounts.User{ID: c.user}})
			if recorder.Code != c.want || !strings.Contains(recorder.Body.String(), `"`+c.code+`"`) || reached {
				t.Fatalf("answer = %d %s, reached the montage center: %v", recorder.Code, recorder.Body, reached)
			}
		})
	}
}

func TestCopySongStreamsTheTrackWithWhatItIs(t *testing.T) {
	var body, header string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body, header = string(data), r.Header.Get(montageapi.SongHeader)
		if user, ok := accounts.UserFrom(r.Context()); !ok || user.ID != "user:alice" {
			t.Errorf("user = %+v", user)
		}
		w.WriteHeader(http.StatusOK)
	})
	library := tracks{t: t, track: music.Track{ID: "t", Title: "Lossless", Format: "ape", Transcode: true, DurationMs: 60_000}}
	handler := montageapi.CopySong(library, serviceproxy.Local(next), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/montage/projects/p/song", strings.NewReader(`{"trackId":"t"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(montageapi.SongHeader, "forged")
	handler(recorder, request, accounts.Session{User: accounts.User{ID: "user:alice"}})
	if recorder.Code != http.StatusOK || body != "song" || header == "forged" || header == "" {
		t.Fatalf("answer = %d; the montage center got %q with %q", recorder.Code, body, header)
	}
	// A track browsers cannot play has no content type: it is decoded to listen.
	var source montage.SongSource
	if err := montageapi.DecodeHeader(header, &source); err != nil || source.Title != "Lossless" || source.ContentType != "" || source.TrackID != "t" {
		t.Fatalf("source = %+v, %v", source, err)
	}
}
