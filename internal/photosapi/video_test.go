package photosapi_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// videoMedia reads every file as a 10-second H.264 video with AAC sound.
type videoMedia struct{}

func (videoMedia) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 10},
		Streams: []ffmpegworker.ProbeStream{{Type: "video", Codec: "h264", PixelFormat: "yuv420p", Width: 1280, Height: 720}, {Type: "audio", Codec: "aac"}}}, nil
}
func (videoMedia) Image(context.Context, *os.File, int) ([]byte, error) {
	return nil, ffmpegworker.ErrUnreadable
}
func (videoMedia) Frame(context.Context, *os.File, float64, int) ([]byte, error) {
	var buffer bytes.Buffer
	err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 16, 9)), nil)
	return buffer.Bytes(), err
}
func (videoMedia) Stream(context.Context, *os.File, ffmpegworker.StreamRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("fragmented mp4"))), nil
}

func TestVideosUploadAndPlayThroughTheAPI(t *testing.T) {
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{Media: videoMedia{}, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	h := photosapi.New(service, nil)
	c := client{t: t, handler: h, as: alice}
	private, _ := librariesOf(c)
	movie := make([]byte, 600)
	binary.BigEndian.PutUint32(movie, 24)
	copy(movie[4:], "ftypisom")
	result := c.upload(private.ID, "", "beach.mp4", movie)
	if result.Code != http.StatusCreated {
		t.Fatalf("upload video: %d %s", result.Code, result.Body.String())
	}
	var video photos.Asset
	if err := json.Unmarshal(result.Body.Bytes(), &video); err != nil || video.Video == nil || video.Video.Duration != 10 || video.MediaType != "video/mp4" {
		t.Fatalf("video = %+v, %v", video, err)
	}
	photo := c.upload(private.ID, "", "a.png", pngBytes(t, 1))
	var still photos.Asset
	_ = json.Unmarshal(photo.Body.Bytes(), &still)
	base := "/api/v1/photos/assets/" + video.ID

	var playback photos.VideoPlayback
	c.json(http.MethodGet, base+"/video/playback?caps=hevc,webm", nil, http.StatusOK, &playback)
	if playback.Mode != photos.PlayDirect || playback.Duration != 10 {
		t.Fatalf("playback = %+v", playback)
	}
	request := httptest.NewRequest(http.MethodGet, base+"/video/original", nil)
	request.Header.Set("Range", "bytes=4-11")
	request = request.WithContext(photosapi.WithPrincipal(request.Context(), alice))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Header().Get("Content-Type") != "video/mp4" || response.Body.String() != "ftypisom" {
		t.Fatalf("original by range: %d %q %q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	stream := c.do(http.MethodGet, base+"/video/stream?start=2.5&transcode=1", nil, "")
	if stream.Code != http.StatusOK || stream.Header().Get("Content-Type") != "video/mp4" || stream.Body.String() != "fragmented mp4" {
		t.Fatalf("stream: %d %s", stream.Code, stream.Body.String())
	}
	for query, code := range map[string]int{"?start=soon": http.StatusBadRequest, "?start=10": http.StatusBadRequest, "?start=-1": http.StatusBadRequest} {
		if got := c.do(http.MethodGet, base+"/video/stream"+query, nil, ""); got.Code != code {
			t.Fatalf("stream%s: %d %s", query, got.Code, got.Body.String())
		}
	}
	poster := c.do(http.MethodGet, base+"/preview", nil, "")
	if poster.Code != http.StatusOK || poster.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("poster: %d %s", poster.Code, poster.Header().Get("Content-Type"))
	}
	if got := c.do(http.MethodGet, "/api/v1/photos/assets/"+still.ID+"/video/playback", nil, ""); got.Code != http.StatusNotFound || !bytes.Contains(got.Body.Bytes(), []byte(`"not_video"`)) {
		t.Fatalf("photo playback: %d %s", got.Code, got.Body.String())
	}
	outsider := client{t: t, handler: h, as: bob}
	for _, path := range []string{"/video/playback", "/video/original", "/video/stream", "/preview"} {
		if got := outsider.do(http.MethodGet, base+path, nil, ""); got.Code != http.StatusNotFound {
			t.Fatalf("%s leaked to another member: %d", path, got.Code)
		}
	}
	assistant := client{t: t, handler: h, as: photosapi.NewAssistantPrincipal(alice.UserID, "alice")}
	if got := assistant.do(http.MethodGet, base, nil, ""); got.Code != http.StatusNotFound {
		t.Fatalf("assistant read a video: %d", got.Code)
	}
}
