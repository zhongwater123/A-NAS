package photosapi_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

var (
	alice = photos.Principal{UserID: "user:alice"}
	bob   = photos.Principal{UserID: "user:bob"}
)

type client struct {
	t       *testing.T
	handler http.Handler
	as      photos.Principal
}

func newAPI(t *testing.T) (http.Handler, *photos.Service) {
	t.Helper()
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return photosapi.New(service, nil), service
}

func (c client) do(method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	c.t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if c.as.UserID != "" {
		request = request.WithContext(photosapi.WithPrincipal(request.Context(), c.as))
	}
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	return recorder
}

func (c client) json(method, path string, body any, wantStatus int, target any) {
	c.t.Helper()
	var encoded []byte
	if body != nil {
		encoded, _ = json.Marshal(body)
	}
	response := c.do(method, path, encoded, "application/json")
	if response.Code != wantStatus {
		c.t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.Code, wantStatus, response.Body.String())
	}
	if target != nil {
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			c.t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
}

func (c client) upload(libraryID, directoryID, name string, content []byte) *httptest.ResponseRecorder {
	c.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if directoryID != "" {
		_ = writer.WriteField("directoryId", directoryID)
	}
	part, _ := writer.CreateFormFile("file", name)
	_, _ = part.Write(content)
	_ = writer.Close()
	return c.do(http.MethodPost, "/api/v1/photos/libraries/"+libraryID+"/uploads", body.Bytes(), writer.FormDataContentType())
}

func pngBytes(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 6, 4))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buffer.Bytes()
}

type libraryList struct {
	Items []photos.Library `json:"items"`
}

func librariesOf(c client) (private, shared photos.Library) {
	var list libraryList
	c.json(http.MethodGet, "/api/v1/photos/libraries", nil, http.StatusOK, &list)
	for _, lib := range list.Items {
		if lib.Kind == photos.LibraryKindShared {
			shared = lib
		} else if lib.OwnerUserID == c.as.UserID {
			private = lib
		}
	}
	return private, shared
}

func TestUploadBrowseAndServeOriginals(t *testing.T) {
	handler, service := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	original := pngBytes(t, 40)

	response := owner.upload(private.ID, "", "cat.png", original)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d; body=%s", response.Code, response.Body.String())
	}
	var asset photos.Asset
	if err := json.Unmarshal(response.Body.Bytes(), &asset); err != nil || asset.Name != "cat.png" || asset.Thumbnail != photos.ThumbnailPending {
		t.Fatalf("uploaded asset = %+v, %v", asset, err)
	}

	var timeline struct {
		Items []photos.Asset `json:"items"`
		Next  string         `json:"next"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/libraries/"+private.ID+"/timeline?limit=10", nil, http.StatusOK, &timeline)
	if len(timeline.Items) != 1 || timeline.Items[0].ID != asset.ID || timeline.Next != "" {
		t.Fatalf("timeline = %+v", timeline)
	}

	thumbnail := owner.do(http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/thumbnail", nil, "")
	if thumbnail.Code != http.StatusNotFound || !strings.Contains(thumbnail.Body.String(), "thumbnail_unavailable") {
		t.Fatalf("pending thumbnail = %d %s", thumbnail.Code, thumbnail.Body.String())
	}
	if _, err := service.ProcessMediaJob(t.Context()); err != nil {
		t.Fatalf("ProcessMediaJob() error = %v", err)
	}
	thumbnail = owner.do(http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/thumbnail", nil, "")
	if thumbnail.Code != http.StatusOK || thumbnail.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("ready thumbnail = %d %v", thumbnail.Code, thumbnail.Header())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/original", nil)
	request.Header.Set("Range", "bytes=0-7")
	request = request.WithContext(photosapi.WithPrincipal(request.Context(), alice))
	ranged := httptest.NewRecorder()
	handler.ServeHTTP(ranged, request)
	if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), original[:8]) {
		t.Fatalf("ranged original = %d %q", ranged.Code, ranged.Body.Bytes())
	}
	if got := ranged.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	// Revalidation compares content, not import time: the thumbnail's ETag
	// differs from the original's and names its derivation version.
	originalTag, thumbnailTag := ranged.Header().Get("ETag"), thumbnail.Header().Get("ETag")
	if originalTag == "" || thumbnailTag == "" || originalTag == thumbnailTag || !strings.Contains(thumbnailTag, "thumbnail/v1") {
		t.Fatalf("ETag original = %q, thumbnail = %q", originalTag, thumbnailTag)
	}
	revalidate := httptest.NewRequest(http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/thumbnail", nil)
	revalidate.Header.Set("If-None-Match", thumbnailTag)
	revalidate = revalidate.WithContext(photosapi.WithPrincipal(revalidate.Context(), alice))
	notModified := httptest.NewRecorder()
	handler.ServeHTTP(notModified, revalidate)
	if notModified.Code != http.StatusNotModified {
		t.Fatalf("revalidated thumbnail status = %d", notModified.Code)
	}
	download := owner.do(http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/original?download=1", nil, "")
	if !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment") || !bytes.Equal(download.Body.Bytes(), original) {
		t.Fatalf("download = %d %v", download.Code, download.Header())
	}

	// Another member cannot tell the photo exists.
	stranger := client{t: t, handler: handler, as: bob}
	for _, path := range []string{"/api/v1/photos/assets/" + asset.ID, "/api/v1/photos/assets/" + asset.ID + "/original", "/api/v1/photos/libraries/" + private.ID + "/timeline"} {
		if response := stranger.do(http.MethodGet, path, nil, ""); response.Code != http.StatusNotFound {
			t.Fatalf("stranger GET %s status = %d", path, response.Code)
		}
	}
}

func TestOrganiseTrashAndCopy(t *testing.T) {
	handler, _ := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, shared := librariesOf(owner)
	var directory photos.Directory
	owner.json(http.MethodPost, "/api/v1/photos/libraries/"+private.ID+"/directories", map[string]string{"name": "Trips"}, http.StatusCreated, &directory)
	var asset photos.Asset
	if err := json.Unmarshal(owner.upload(private.ID, directory.ID, "beach.png", pngBytes(t, 50)).Body.Bytes(), &asset); err != nil || asset.DirectoryID != directory.ID {
		t.Fatalf("upload into directory = %+v, %v", asset, err)
	}

	var listing photos.DirectoryListing
	owner.json(http.MethodGet, "/api/v1/photos/libraries/"+private.ID+"/entries", nil, http.StatusOK, &listing)
	if len(listing.Directories) != 1 || len(listing.Assets) != 0 {
		t.Fatalf("root listing = %+v", listing)
	}
	var changed photos.Asset
	owner.json(http.MethodPatch, "/api/v1/photos/assets/"+asset.ID, map[string]string{"name": "sea.png", "directoryId": ""}, http.StatusOK, &changed)
	if changed.ID != asset.ID || changed.Name != "sea.png" || changed.DirectoryID != "" {
		t.Fatalf("changed asset = %+v", changed)
	}
	owner.json(http.MethodPatch, "/api/v1/photos/directories/"+directory.ID, map[string]string{"name": "Holidays"}, http.StatusOK, &directory)
	owner.json(http.MethodPatch, "/api/v1/photos/assets/"+asset.ID, map[string]string{}, http.StatusBadRequest, nil)
	owner.json(http.MethodPost, "/api/v1/photos/libraries/"+private.ID+"/directories", map[string]string{"name": "Holidays"}, http.StatusConflict, nil)

	var copied photos.Asset
	owner.json(http.MethodPost, "/api/v1/photos/assets/"+asset.ID+"/copies", map[string]string{"libraryId": shared.ID}, http.StatusCreated, &copied)
	member := client{t: t, handler: handler, as: bob}
	member.json(http.MethodDelete, "/api/v1/photos/assets/"+copied.ID, nil, http.StatusForbidden, nil)

	owner.json(http.MethodDelete, "/api/v1/photos/assets/"+asset.ID, nil, http.StatusOK, &asset)
	if asset.Trash == nil {
		t.Fatalf("trashed asset has no trash state")
	}
	var trash struct {
		Items []photos.Asset `json:"items"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/libraries/"+private.ID+"/trash", nil, http.StatusOK, &trash)
	if len(trash.Items) != 1 {
		t.Fatalf("trash = %+v", trash)
	}
	owner.json(http.MethodPost, "/api/v1/photos/assets/"+asset.ID+"/restore", nil, http.StatusOK, &asset)
	owner.json(http.MethodDelete, "/api/v1/photos/assets/"+asset.ID, nil, http.StatusOK, nil)
	var emptied struct {
		Purged int `json:"purged"`
	}
	owner.json(http.MethodDelete, "/api/v1/photos/libraries/"+private.ID+"/trash", nil, http.StatusOK, &emptied)
	if emptied.Purged != 1 {
		t.Fatalf("empty trash purged %d", emptied.Purged)
	}
	owner.json(http.MethodDelete, "/api/v1/photos/directories/"+directory.ID, nil, http.StatusNoContent, nil)
	// The shared copy survives its source.
	member.json(http.MethodGet, "/api/v1/photos/assets/"+copied.ID, nil, http.StatusOK, nil)
}

func TestRejectsUnsupportedUploadsAndBadRequests(t *testing.T) {
	handler, _ := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	if response := owner.upload(private.ID, "", "notes.txt", []byte("plain text, not a photo")); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text upload status = %d", response.Code)
	}
	if response := owner.do(http.MethodGet, "/api/v1/photos/libraries/"+private.ID+"/timeline?cursor=nope", nil, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor status = %d", response.Code)
	}
	if response := owner.do(http.MethodPost, "/api/v1/photos/libraries/"+private.ID+"/directories", []byte(`{"name":"a"}`), "text/plain"); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON body status = %d", response.Code)
	}
	if response := owner.do(http.MethodPost, "/api/v1/photos/libraries/"+private.ID+"/directories", []byte(`{"name":"a","extra":1}`), "application/json"); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.Code)
	}
}

func TestRequiresPrincipalAndService(t *testing.T) {
	handler, _ := newAPI(t)
	if response := (client{t: t, handler: handler}).do(http.MethodGet, "/api/v1/photos/libraries", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", response.Code)
	}
	disabled := client{t: t, handler: photosapi.New(nil, nil), as: alice}
	if response := disabled.do(http.MethodGet, "/api/v1/photos/libraries", nil, ""); response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "photos_unavailable") {
		t.Fatalf("disabled status = %d %s", response.Code, response.Body.String())
	}
}

func TestHiddenItemsAnswerExactlyLikeMissingOnes(t *testing.T) {
	handler, _ := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	var asset photos.Asset
	if err := json.Unmarshal(owner.upload(private.ID, "", "secret.png", pngBytes(t, 70)).Body.Bytes(), &asset); err != nil {
		t.Fatal(err)
	}
	stranger := client{t: t, handler: handler, as: bob}
	for _, path := range []string{
		"/api/v1/photos/assets/%s",
		"/api/v1/photos/assets/%s/original",
		"/api/v1/photos/assets/%s/thumbnail",
	} {
		hidden := stranger.do(http.MethodGet, strings.Replace(path, "%s", asset.ID, 1), nil, "")
		missing := stranger.do(http.MethodGet, strings.Replace(path, "%s", "photo:missing", 1), nil, "")
		if hidden.Code != missing.Code || hidden.Body.String() != missing.Body.String() {
			t.Errorf("%s: hidden %d %q, missing %d %q", path, hidden.Code, hidden.Body.String(), missing.Code, missing.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/photos/libraries/%s/timeline", "/api/v1/photos/libraries/%s/timeline/months", "/api/v1/photos/libraries/%s/entries", "/api/v1/photos/libraries/%s/trash"} {
		hidden := stranger.do(http.MethodGet, strings.Replace(path, "%s", private.ID, 1), nil, "")
		missing := stranger.do(http.MethodGet, strings.Replace(path, "%s", "library:missing", 1), nil, "")
		if hidden.Code != http.StatusNotFound || hidden.Body.String() != missing.Body.String() {
			t.Errorf("%s: hidden %d %q, missing %d %q", path, hidden.Code, hidden.Body.String(), missing.Code, missing.Body.String())
		}
	}
}

func TestTimelineByMonthAndClustersWithoutLocalAI(t *testing.T) {
	handler, _ := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	if response := owner.upload(private.ID, "", "cat.png", pngBytes(t, 40)); response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", response.Code)
	}
	var months struct {
		Items []photos.TimelineMonth `json:"items"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/libraries/"+private.ID+"/timeline/months", nil, http.StatusOK, &months)
	if len(months.Items) != 1 || months.Items[0].Photos != 1 {
		t.Fatalf("months = %+v", months)
	}
	var timeline struct {
		Items []photos.Asset `json:"items"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/libraries/"+private.ID+"/timeline?month="+months.Items[0].Month, nil, http.StatusOK, &timeline)
	if len(timeline.Items) != 1 || timeline.Items[0].Name != "cat.png" {
		t.Fatalf("timeline of %s = %+v", months.Items[0].Month, timeline)
	}
	if response := owner.do(http.MethodGet, "/api/v1/photos/libraries/"+private.ID+"/timeline?month=2026-13", nil, ""); response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "invalid_month") {
		t.Fatalf("bad month = %d %s", response.Code, response.Body.String())
	}
	// Without local AI there are no clusters yet, and a label without one is
	// not found.
	var labels struct {
		Items []photos.LabelCount `json:"items"`
		Ready bool                `json:"ready"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/labels", nil, http.StatusOK, &labels)
	if labels.Ready || labels.Items == nil || len(labels.Items) != 0 {
		t.Fatalf("labels without local AI = %+v", labels)
	}
	var cluster struct {
		Items []photos.Asset `json:"items"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/labels/cat/assets", nil, http.StatusOK, &cluster)
	if cluster.Items == nil || len(cluster.Items) != 0 {
		t.Fatalf("cat cluster without local AI = %+v", cluster)
	}
	owner.json(http.MethodGet, "/api/v1/photos/labels/no-such-label/assets", nil, http.StatusNotFound, nil)
}

func TestAIStatusReportsProgressOverVisiblePhotos(t *testing.T) {
	handler, service := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	if response := owner.upload(private.ID, "", "cat.png", pngBytes(t, 40)); response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", response.Code)
	}
	if _, err := service.ProcessMediaJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status photos.AIStatus
	owner.json(http.MethodGet, "/api/v1/photos/ai", nil, http.StatusOK, &status)
	if status.State != photos.AIUnavailable || status.Pending != 1 || status.Ready != 0 {
		t.Fatalf("AI status without a Worker = %+v", status)
	}
	stranger := client{t: t, handler: handler, as: bob}
	stranger.json(http.MethodGet, "/api/v1/photos/ai", nil, http.StatusOK, &status)
	if status.Pending != 0 {
		t.Fatalf("another member's AI status counts alice's photo: %+v", status)
	}
}

func TestSearchMatchesNamesWithoutLocalAI(t *testing.T) {
	handler, _ := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	for _, name := range []string{"海边的猫.png", "garage.png"} {
		if response := owner.upload(private.ID, "", name, pngBytes(t, uint8(len(name)))); response.Code != http.StatusCreated {
			t.Fatalf("upload %s status = %d", name, response.Code)
		}
	}
	var results struct {
		Items    []photos.Asset `json:"items"`
		Semantic bool           `json:"semantic"`
		Closest  int            `json:"closest"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/search?q="+url.QueryEscape("海边"), nil, http.StatusOK, &results)
	if results.Semantic || len(results.Items) != 1 || results.Items[0].Name != "海边的猫.png" || results.Closest != 1 {
		t.Fatalf("search without an AI Worker = %+v", results)
	}
	client{t: t, handler: handler, as: bob}.json(http.MethodGet, "/api/v1/photos/search?q="+url.QueryEscape("海边"), nil, http.StatusOK, &results)
	if len(results.Items) != 0 {
		t.Fatalf("another member finds %+v", results.Items)
	}
	for _, query := range []string{"", "?q=", "?q=" + strings.Repeat("猫", photos.MaxQueryRunes+1)} {
		if response := owner.do(http.MethodGet, "/api/v1/photos/search"+query, nil, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("search%s status = %d, want 400", query, response.Code)
		}
	}
}

func TestAlbumsTagsAndCorrections(t *testing.T) {
	handler, _ := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	var photo photos.Asset
	if response := owner.upload(private.ID, "", "cat.png", pngBytes(t, 50)); response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", response.Code)
	} else if err := json.Unmarshal(response.Body.Bytes(), &photo); err != nil {
		t.Fatal(err)
	}

	var album photos.Album
	owner.json(http.MethodPost, "/api/v1/photos/libraries/"+url.PathEscape(private.ID)+"/albums", map[string]string{"name": "旅行"}, http.StatusCreated, &album)
	owner.json(http.MethodPost, "/api/v1/photos/albums/"+url.PathEscape(album.ID)+"/assets", map[string]string{"assetId": photo.ID}, http.StatusOK, nil)
	var listed struct {
		Items []photos.Asset `json:"items"`
	}
	owner.json(http.MethodGet, "/api/v1/photos/albums/"+url.PathEscape(album.ID)+"/assets", nil, http.StatusOK, &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != photo.ID {
		t.Fatalf("album photos = %+v", listed.Items)
	}

	var tagged photos.Asset
	owner.json(http.MethodPost, "/api/v1/photos/assets/"+url.PathEscape(photo.ID)+"/tags", map[string]string{"name": "猫咪/小黑"}, http.StatusUnprocessableEntity, nil)
	owner.json(http.MethodPost, "/api/v1/photos/assets/"+url.PathEscape(photo.ID)+"/tags", map[string]string{"name": "小黑"}, http.StatusOK, &tagged)
	if len(tagged.Tags) != 1 || len(tagged.Albums) != 1 || tagged.Albums[0].Name != "旅行" {
		t.Fatalf("details after tagging = %+v", tagged)
	}
	var untagged photos.Asset
	owner.json(http.MethodDelete, "/api/v1/photos/assets/"+url.PathEscape(photo.ID)+"/tags/"+url.PathEscape("小黑"), nil, http.StatusOK, &untagged)
	if untagged.ID != photo.ID || len(untagged.Tags) != 0 {
		t.Fatalf("details after removing the tag = %+v", untagged)
	}
	owner.json(http.MethodDelete, "/api/v1/photos/assets/"+url.PathEscape(photo.ID)+"/ai-labels/no-such-label", nil, http.StatusNotFound, nil)
	owner.json(http.MethodDelete, "/api/v1/photos/assets/"+url.PathEscape(photo.ID)+"/ai-labels/cat", nil, http.StatusOK, nil)

	client{t: t, handler: handler, as: bob}.json(http.MethodGet, "/api/v1/photos/albums/"+url.PathEscape(album.ID)+"/assets", nil, http.StatusNotFound, nil)
	owner.json(http.MethodDelete, "/api/v1/photos/albums/"+url.PathEscape(album.ID)+"/assets/"+url.PathEscape(photo.ID), nil, http.StatusNoContent, nil)
	owner.json(http.MethodDelete, "/api/v1/photos/albums/"+url.PathEscape(album.ID), nil, http.StatusNoContent, nil)
	owner.json(http.MethodGet, "/api/v1/photos/assets/"+url.PathEscape(photo.ID), nil, http.StatusOK, nil)
}
