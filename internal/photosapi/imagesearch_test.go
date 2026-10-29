package photosapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// shadeEmbedder is an AI Worker that sees only how light an image is.
type shadeEmbedder struct{}

func (shadeEmbedder) Info(context.Context) (photos.EmbedderInfo, error) {
	return photos.EmbedderInfo{Model: "shade-1", Dimensions: 2}, nil
}

func (shadeEmbedder) EmbedImage(_ context.Context, file *os.File) ([]float32, error) {
	img, err := jpeg.Decode(file)
	if err != nil {
		return nil, photos.ErrAIRejected
	}
	r, _, _, _ := img.At(0, 0).RGBA()
	light := float64(r) / 0xffff
	norm := math.Hypot(light, 1-light)
	return []float32{float32(light / norm), float32((1 - light) / norm)}, nil
}

func (shadeEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}

type openGate struct{}

func (openGate) Open(context.Context, uint64) (bool, string) { return true, "" }

func (c client) searchByImage(content []byte) *httptest.ResponseRecorder {
	c.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if content != nil {
		part, _ := writer.CreateFormFile("file", "query.png")
		_, _ = part.Write(content)
	}
	_ = writer.Close()
	return c.do(http.MethodPost, "/api/v1/photos/image-searches", body.Bytes(), writer.FormDataContentType())
}

func TestSearchByAnImageFromOutsideTheLibrary(t *testing.T) {
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{DisableCapacityReserve: true, AI: shadeEmbedder{}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	handler := photosapi.New(service, nil)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	for name, shade := range map[string]uint8{"dark.png": 30, "grey.png": 120, "light.png": 250} {
		if response := owner.upload(private.ID, "", name, pngBytes(t, shade)); response.Code != http.StatusCreated {
			t.Fatalf("upload %s status = %d", name, response.Code)
		}
	}
	ctx := context.Background()
	for processed := true; processed; {
		if processed, err = service.ProcessMediaJob(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for processed := true; processed; {
		if processed, err = service.ProcessAIJob(ctx, shadeEmbedder{}, openGate{}); err != nil {
			t.Fatal(err)
		}
	}

	response := owner.searchByImage(pngBytes(t, 240))
	if response.Code != http.StatusCreated {
		t.Fatalf("POST image-searches status = %d; body=%s", response.Code, response.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("POST image-searches body = %s", response.Body.String())
	}
	var results struct {
		Items []photos.Asset `json:"items"`
		Next  string         `json:"next"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/image-searches/"+created.ID+"/assets", nil, http.StatusOK, &results)
	var got []string
	for _, asset := range results.Items {
		got = append(got, asset.Name)
	}
	if want := []string{"light.png", "grey.png", "dark.png"}; !slices.Equal(got, want) || results.Next != "" {
		t.Fatalf("results = %v (next %q), want %v", got, results.Next, want)
	}

	for name, check := range map[string]struct {
		response *httptest.ResponseRecorder
		status   int
		code     string
	}{
		"text":            {owner.searchByImage([]byte("not an image")), http.StatusUnsupportedMediaType, "unsupported_media_type"},
		"no file":         {owner.searchByImage(nil), http.StatusBadRequest, "file_required"},
		"unknown search":  {owner.do(http.MethodGet, "/api/v1/photos/image-searches/0123/assets", nil, ""), http.StatusNotFound, "search_expired"},
		"another member":  {client{t: t, handler: handler, as: bob}.do(http.MethodGet, "/api/v1/photos/image-searches/"+created.ID+"/assets", nil, ""), http.StatusNotFound, "search_expired"},
		"bad limit":       {owner.do(http.MethodGet, "/api/v1/photos/image-searches/"+created.ID+"/assets?limit=0", nil, ""), http.StatusBadRequest, "invalid_request"},
		"not a multipart": {owner.do(http.MethodPost, "/api/v1/photos/image-searches", []byte("{}"), "application/json"), http.StatusBadRequest, "invalid_multipart"},
	} {
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(check.response.Body.Bytes(), &body)
		if check.response.Code != check.status || body.Error.Code != check.code {
			t.Errorf("%s: status %d code %q, want %d %q", name, check.response.Code, body.Error.Code, check.status, check.code)
		}
	}
}

func TestSearchByAnImageWithoutLocalAI(t *testing.T) {
	handler, _ := newAPI(t)
	response := client{t: t, handler: handler, as: alice}.searchByImage(pngBytes(t, 240))
	if response.Code != http.StatusServiceUnavailable || !bytes.Contains(response.Body.Bytes(), []byte(`"ai_unavailable"`)) {
		t.Fatalf("POST image-searches without local AI = %d %s, want 503 ai_unavailable", response.Code, response.Body.String())
	}
}
