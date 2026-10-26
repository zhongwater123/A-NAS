package httpapi_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/openapitest"
)

// silentMP3 is an ID3v2.3 title followed by silent MPEG-1 Layer III frames.
func silentMP3(title string, frames int) []byte {
	body := []byte("TIT2")
	body = binary.BigEndian.AppendUint32(body, uint32(len(title)+1))
	body = append(append(body, 0, 0, 3), title...)
	size := len(body)
	file := append([]byte{'I', 'D', '3', 3, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}, body...)
	for range frames {
		frame := make([]byte, 417)
		copy(frame, []byte{0xff, 0xfb, 0x90, 0x00})
		file = append(file, frame...)
	}
	return file
}

type contractClient struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
	// forwardedFor is the LAN address the client signed in from, which its
	// requests keep coming from through Caddy (ADR 0012).
	forwardedFor string
}

// do sends a request, checks its status and validates request and response
// against the OpenAPI contract.
func (c *contractClient) do(method, path, body string, headers map[string]string, want int) *httptest.ResponseRecorder {
	c.t.Helper()
	newRequest := func() *http.Request {
		request := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
		if c.cookie != nil {
			request.AddCookie(c.cookie)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if method != http.MethodGet && c.csrf != "" {
			request.Header.Set("X-CSRF-Token", c.csrf)
		}
		if c.forwardedFor != "" {
			request.Header.Set("X-Forwarded-For", c.forwardedFor)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		return request
	}
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, newRequest())
	if recorder.Code != want {
		c.t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, recorder.Code, want, recorder.Body.String())
	}
	openapitest.Check(c.t, newRequest(), recorder)
	return recorder
}

func TestMusicAPIScansStreamsAndKeepsUserData(t *testing.T) {
	dir := t.TempDir()
	store, err := accounts.OpenSQLite(filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	fileStore, err := files.OpenSQLite(filepath.Join(dir, "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	volume := filepath.Join(dir, "volume")
	fileService := files.NewService(fileStore, volume, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	musicStore, err := music.OpenSQLite(filepath.Join(dir, "music.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = musicStore.Close() })
	library := music.New(musicStore, filepath.Join(dir, "covers"), fileService, accountService, music.Options{Worker: decodingWorker{}})
	t.Cleanup(library.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService, Files: fileService,
		Music: library, Logger: logger,
	})
	client := &contractClient{t: t, handler: handler}

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/v1/music/tracks", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated tracks status = %d", unauthenticated.Code)
	}
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", bytes.NewBufferString(`{"username":"owner","password":"correct horse battery staple"}`))
	setup.Header.Set("Content-Type", "application/json")
	fromLocalConsole(setup)
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, setup)
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil || created.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", created.Code, created.Body.String())
	}
	client.cookie, client.csrf = created.Result().Cookies()[0], session.CSRFToken

	song := filepath.Join(volume, "spaces", "shared", "Music", "song.mp3")
	if err := os.MkdirAll(filepath.Dir(song), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(song, silentMP3("Song", 40), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(song), "lossless.ape"), []byte("MAC "), 0o644); err != nil {
		t.Fatal(err)
	}

	csrf := client.csrf
	client.csrf = ""
	missingCSRF := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/music/library/scan", nil)
	request.AddCookie(client.cookie)
	handler.ServeHTTP(missingCSRF, request)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("scan without CSRF status = %d", missingCSRF.Code)
	}
	client.csrf = csrf

	client.do(http.MethodPost, "/api/v1/music/library/scan", "", nil, http.StatusAccepted)
	deadline := time.Now().Add(10 * time.Second)
	var status music.Status
	for {
		if err := json.Unmarshal(client.do(http.MethodGet, "/api/v1/music/library", "", nil, http.StatusOK).Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.Scan.State == "idle" || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.Tracks != 2 {
		t.Fatalf("library status = %+v", status)
	}
	var tracks music.Library
	if err := json.Unmarshal(client.do(http.MethodGet, "/api/v1/music/tracks", "", nil, http.StatusOK).Body.Bytes(), &tracks); err != nil || len(tracks.Items) != 2 {
		t.Fatalf("tracks = %+v, %v", tracks, err)
	}
	var id, decodedID string
	for _, track := range tracks.Items {
		if track.Transcode {
			decodedID = track.ID
		} else {
			id = track.ID
		}
	}
	decoded := client.do(http.MethodGet, "/api/v1/music/tracks/"+decodedID+"/stream?start=12.5", "", map[string]string{"Range": "bytes=0-"}, http.StatusOK)
	if decoded.Body.String() != "fLaC from 12.5" || decoded.Header().Get("Content-Type") != "audio/flac" || decoded.Header().Get("Accept-Ranges") != "none" {
		t.Errorf("decoded stream = %q as %q", decoded.Body.String(), decoded.Header().Get("Content-Type"))
	}
	client.do(http.MethodGet, "/api/v1/music/tracks/"+decodedID+"/stream?start=999", "", nil, http.StatusUnprocessableEntity)

	partial := client.do(http.MethodGet, "/api/v1/music/tracks/"+id+"/stream", "", map[string]string{"Range": "bytes=0-2"}, http.StatusPartialContent)
	if partial.Body.String() != "ID3" || partial.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("stream range = %q as %q", partial.Body.String(), partial.Header().Get("Content-Type"))
	}
	client.do(http.MethodGet, "/api/v1/music/tracks/file:missing/stream", "", nil, http.StatusNotFound)
	client.do(http.MethodGet, "/api/v1/music/tracks/"+id+"/lyrics", "", nil, http.StatusNotFound)
	client.do(http.MethodGet, "/api/v1/music/covers/0123456789abcdef0123456789abcdef", "", nil, http.StatusNotFound)
	// The contract rejects this request, so it is sent without validation.
	badSize := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/music/covers/0123456789abcdef0123456789abcdef?size=huge", nil)
	request.AddCookie(client.cookie)
	handler.ServeHTTP(badSize, request)
	if badSize.Code != http.StatusBadRequest {
		t.Errorf("cover size huge status = %d", badSize.Code)
	}

	client.do(http.MethodPut, "/api/v1/music/favorites/"+id, "", nil, http.StatusNoContent)
	client.do(http.MethodGet, "/api/v1/music/favorites", "", nil, http.StatusOK)
	client.do(http.MethodDelete, "/api/v1/music/favorites/"+id, "", nil, http.StatusNoContent)
	client.do(http.MethodPost, "/api/v1/music/plays", `{"trackId":"`+id+`"}`, nil, http.StatusNoContent)
	client.do(http.MethodGet, "/api/v1/music/recent", "", nil, http.StatusOK)
	client.do(http.MethodDelete, "/api/v1/music/recent", "", nil, http.StatusNoContent)

	var playlist music.Playlist
	if err := json.Unmarshal(client.do(http.MethodPost, "/api/v1/music/playlists", `{"name":"Mix","trackIds":["`+id+`"]}`, nil, http.StatusCreated).Body.Bytes(), &playlist); err != nil {
		t.Fatal(err)
	}
	client.do(http.MethodPost, "/api/v1/music/playlists/"+playlist.ID+"/tracks", `{"trackIds":["`+id+`"]}`, nil, http.StatusOK)
	client.do(http.MethodPatch, "/api/v1/music/playlists/"+playlist.ID, `{"name":"Evening","trackIds":[]}`, nil, http.StatusOK)
	client.do(http.MethodPatch, "/api/v1/music/playlists/"+playlist.ID, `{"name":" "}`, nil, http.StatusUnprocessableEntity)
	client.do(http.MethodGet, "/api/v1/music/playlists", "", nil, http.StatusOK)
	client.do(http.MethodDelete, "/api/v1/music/playlists/"+playlist.ID, "", nil, http.StatusNoContent)
	client.do(http.MethodDelete, "/api/v1/music/playlists/"+playlist.ID, "", nil, http.StatusNotFound)

	client.do(http.MethodGet, "/api/v1/music/sources", "", nil, http.StatusOK)
	client.do(http.MethodPut, "/api/v1/music/sources", `{"custom":true,"items":[{"spaceId":"space:unknown"}]}`, nil, http.StatusNotFound)
	client.do(http.MethodPut, "/api/v1/music/sources", `{"custom":false}`, nil, http.StatusOK)
}

// decodingWorker stands in for the media worker: an APE file probes as a
// one-minute song, and decoding answers with where it started.
type decodingWorker struct{ mediaworker.Worker }

func (decodingWorker) Probe(context.Context, *os.File) (mediaworker.Probe, error) {
	return mediaworker.Probe{Format: mediaworker.ProbeFormat{Name: "ape", Duration: 60, Tags: map[string]string{"title": "Lossless"}}}, nil
}

func (decodingWorker) Audio(_ context.Context, _ *os.File, request mediaworker.AudioRequest) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("fLaC from " + strconv.FormatFloat(request.Start, 'f', -1, 64))), nil
}
