package protocol_test

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
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocut/protocol"
)

// tracks stands in for the music library: one track, of Alice's, whose file
// fails to open with opens.
type tracks struct {
	t     *testing.T
	track music.Track
	opens error
}

func (l tracks) OpenTrack(_ context.Context, actor accounts.User, id string) (music.Stream, error) {
	if actor.ID != "user:alice" || id != l.track.ID {
		return music.Stream{}, music.ErrNotFound
	}
	if l.opens != nil {
		return music.Stream{}, l.opens
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
		opens error
		user  string
		want  int
		code  string
	}{
		"another member's track": {music.Track{ID: "t"}, nil, "user:bob", http.StatusNotFound, "not_found"},
		"larger than 100 MiB":    {music.Track{ID: "t", SizeBytes: app.MaxSongBytes + 1}, nil, "user:alice", http.StatusRequestEntityTooLarge, "song_too_large"},
		"shorter than 30 s":      {music.Track{ID: "t", DurationMs: 29_000}, nil, "user:alice", http.StatusUnprocessableEntity, "song_length"},
		"longer than 8 min":      {music.Track{ID: "t", DurationMs: 480_001}, nil, "user:alice", http.StatusUnprocessableEntity, "song_length"},
		// What changed since the library was scanned answers as it does
		// when the music library plays the track, not as a failure of the
		// library.
		"a file deleted since the scan": {music.Track{ID: "t"}, files.ErrNotFound, "user:alice", http.StatusNotFound, "not_found"},
		"a file she may no longer open": {music.Track{ID: "t"}, accounts.ErrForbidden, "user:alice", http.StatusForbidden, "forbidden"},
		"a library that cannot open it": {music.Track{ID: "t"}, io.ErrUnexpectedEOF, "user:alice", http.StatusServiceUnavailable, "music_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			handler := protocol.CopySong(tracks{t: t, track: c.track, opens: c.opens}, next, nil)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/api/v1/videocut/projects/p/song", strings.NewReader(`{"trackId":"t"}`))
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
		body, header = string(data), r.Header.Get(protocol.SongHeader)
		if user, ok := accounts.UserFrom(r.Context()); !ok || user.ID != "user:alice" {
			t.Errorf("user = %+v", user)
		}
		w.WriteHeader(http.StatusOK)
	})
	library := tracks{t: t, track: music.Track{ID: "t", Title: "Lossless", Format: "ape", Transcode: true, DurationMs: 60_000}}
	handler := protocol.CopySong(library, serviceproxy.Local(next), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/videocut/projects/p/song", strings.NewReader(`{"trackId":"t"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(protocol.SongHeader, "forged")
	handler(recorder, request, accounts.Session{User: accounts.User{ID: "user:alice"}})
	if recorder.Code != http.StatusOK || body != "song" || header == "forged" || header == "" {
		t.Fatalf("answer = %d; the montage center got %q with %q", recorder.Code, body, header)
	}
	// A track browsers cannot play has no content type: it is decoded to listen.
	var source domain.SongSource
	if err := protocol.DecodeHeader(header, &source); err != nil || source.Title != "Lossless" || source.ContentType != "" || source.TrackID != "t" {
		t.Fatalf("source = %+v, %v", source, err)
	}
}

// The music library does not bound a title tag; the header's reader takes at
// most 4 KiB. The title is cut to what the montage center keeps before it is
// sent, so a long one does not make the song impossible to choose.
func TestCopySongCutsALongTitleToFitItsHeader(t *testing.T) {
	var header string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get(protocol.SongHeader)
		w.WriteHeader(http.StatusOK)
	})
	library := tracks{t: t, track: music.Track{ID: "t", Title: strings.Repeat("长", 1500), Format: "mp3", DurationMs: 60_000}}
	handler := protocol.CopySong(library, serviceproxy.Local(next), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/videocut/projects/p/song", strings.NewReader(`{"trackId":"t"}`))
	request.Header.Set("Content-Type", "application/json")
	handler(recorder, request, accounts.Session{User: accounts.User{ID: "user:alice"}})
	var source domain.SongSource
	if err := protocol.DecodeHeader(header, &source); recorder.Code != http.StatusOK || err != nil || source.Title != strings.Repeat("长", 200) {
		t.Fatalf("answer = %d; the title arrived with %d runes, %v", recorder.Code, len([]rune(source.Title)), err)
	}
}
