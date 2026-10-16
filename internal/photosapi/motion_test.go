package photosapi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

type motionMedia struct{}

func (motionMedia) Probe(context.Context, *os.File) (mediaworker.Probe, error) {
	return mediaworker.Probe{Format: mediaworker.ProbeFormat{Name: "mov", Duration: 2}, Streams: []mediaworker.ProbeStream{{Type: "video", Width: 16, Height: 16}}}, nil
}
func (motionMedia) Image(context.Context, *os.File, int) ([]byte, error) {
	return nil, mediaworker.ErrUnreadable
}
func (motionMedia) Stream(context.Context, *os.File, mediaworker.StreamRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("mp4"))), nil
}
func TestLivePhotoAPIExportRangeAndPolicy(t *testing.T) {
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{Media: motionMedia{}, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	h := photosapi.New(service, nil)
	c := client{t: t, handler: h, as: alice}
	private, _ := librariesOf(c)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	photo := pngBytes(t, 80)
	movie := make([]byte, 64)
	binary.BigEndian.PutUint32(movie, 24)
	copy(movie[4:], "ftypqt  ")
	part, _ := writer.CreateFormFile("file", "live.png")
	part.Write(photo)
	part, _ = writer.CreateFormFile("motion", "live.MOV")
	part.Write(movie)
	writer.Close()
	result := c.do(http.MethodPost, "/api/v1/photos/libraries/"+private.ID+"/live-photos", body.Bytes(), writer.FormDataContentType())
	if result.Code != 201 {
		t.Fatalf("upload: %d %s", result.Code, result.Body.String())
	}
	var asset photos.Asset
	if err := json.Unmarshal(result.Body.Bytes(), &asset); err != nil || asset.Motion == nil {
		t.Fatalf("asset: %+v %v", asset, err)
	}
	base := "/api/v1/photos/assets/" + asset.ID
	for _, path := range []string{"/motion", "/motion/play", "/download", "/preview"} {
		outsider := client{t: t, handler: h, as: bob}
		if got := outsider.do("GET", base+path, nil, ""); got.Code != 404 {
			t.Fatalf("%s leaked: %d", path, got.Code)
		}
	}
	request := httptest.NewRequest("GET", base+"/motion", nil)
	request.Header.Set("Range", "bytes=8-15")
	request = request.WithContext(photosapi.WithPrincipal(request.Context(), alice))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 206 || !bytes.Equal(response.Body.Bytes(), movie[8:16]) {
		t.Fatalf("range: %d %s", response.Code, response.Body.String())
	}
	c.json(http.MethodPatch, base, map[string]string{"name": "renamed"}, http.StatusOK, nil)
	download := c.do("GET", base+"/download", nil, "")
	archive, err := zip.NewReader(bytes.NewReader(download.Body.Bytes()), int64(download.Body.Len()))
	if err != nil || len(archive.File) != 2 {
		t.Fatalf("zip: %v %s", err, download.Body.String())
	}
	for i, expected := range [][]byte{photo, movie} {
		reader, _ := archive.File[i].Open()
		got, _ := io.ReadAll(reader)
		reader.Close()
		if !bytes.Equal(got, expected) {
			t.Fatal("export changed original")
		}
	}
	if archive.File[0].Name != "renamed.png" || archive.File[1].Name != "renamed.MOV" {
		t.Fatal("unpaired export names")
	}
}

func TestLiveVideoAloneNeedsItsAppleIdentifier(t *testing.T) {
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{Media: motionMedia{}, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	c := client{t: t, handler: photosapi.New(service, nil), as: alice}
	private, _ := librariesOf(c)
	send := func(name string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if name != "" {
			part, _ := writer.CreateFormFile("file", name)
			movie := make([]byte, 64)
			binary.BigEndian.PutUint32(movie, 24)
			copy(movie[4:], "ftypqt  ")
			part.Write(movie)
		}
		writer.Close()
		return c.do(http.MethodPost, "/api/v1/photos/libraries/"+private.ID+"/live-videos", body.Bytes(), writer.FormDataContentType())
	}
	// The stand-in decoder reports no Apple identifier: nothing to match.
	if result := send("IMG_1.MOV"); result.Code != http.StatusUnprocessableEntity || !bytes.Contains(result.Body.Bytes(), []byte(`"invalid_motion_photo"`)) {
		t.Fatalf("MOV without identifier: %d %s", result.Code, result.Body.String())
	}
	if result := send(""); result.Code != http.StatusBadRequest || !bytes.Contains(result.Body.Bytes(), []byte(`"file_required"`)) {
		t.Fatalf("no file: %d %s", result.Code, result.Body.String())
	}
}
