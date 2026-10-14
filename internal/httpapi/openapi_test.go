package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	fakeappstore "github.com/zhongwater123/A-NAS/internal/appstore/fake"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
	fakecontainers "github.com/zhongwater123/A-NAS/internal/containers/fake"
	"github.com/zhongwater123/A-NAS/internal/containersapi"
	"github.com/zhongwater123/A-NAS/internal/feedback"
	"github.com/zhongwater123/A-NAS/internal/feedbackapi"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/mediaapi"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/terminal"
	"github.com/zhongwater123/A-NAS/internal/transfers"
	"github.com/zhongwater123/A-NAS/internal/transfersapi"
)

func TestOpenAPIContractMatchesHTTPResponses(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	if err := document.Validate(ctx); err != nil {
		t.Fatalf("validate OpenAPI document: %v", err)
	}
	router, err := legacyrouter.NewRouter(document)
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}

	photoHandler, photoLibraryID, photoAssetID := photoAPI(t)
	movies := newMediaFixture(t)
	mediaHandler := mediaAPIFor(movies.media, movies.admin)
	mediaVideo := "/api/v1/media/videos/" + movies.film
	feedbackHandler, feedbackItem := feedbackAPI(t)
	transfer := transfersAPI(t)
	tusHeaders := func(headers map[string]string) map[string]string {
		headers["Tus-Resumable"] = "1.0.0"
		return headers
	}
	uploadMetadata := func(name string) string {
		return "filename " + base64.StdEncoding.EncodeToString([]byte(name)) + ",spaceId " + base64.StdEncoding.EncodeToString([]byte(transfer.space))
	}
	feedbackPath := "/api/v1/feedback/" + feedbackItem.ID
	created, createdType := feedbackForm{text: "相册里的视频无法播放", images: [][]byte{feedbackPNG(t)}}.encode(t)
	edit, editType := feedbackForm{text: "改为：MKV 无法播放", version: feedbackItem.Version, keep: []string{feedbackItem.Images[0].ID}}.encode(t)
	stale, staleType := feedbackForm{text: "旧版本", version: 99}.encode(t)
	svg, svgType := feedbackForm{images: [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)}}.encode(t)
	reply := feedbackItem.Comments[0]
	replyPath := feedbackPath + "/comments/" + reply.ID
	comment, commentType := feedbackForm{text: "我这边也能复现"}.encode(t)
	commentEdit, commentEditType := feedbackForm{text: "已在 1.0.2 修复", version: reply.Version, keep: []string{reply.Images[0].ID}}.encode(t)
	commentStale, commentStaleType := feedbackForm{text: "旧版本", version: 99}.encode(t)
	searchImage, searchImageType := imageSearchForm(t)
	tests := []struct {
		name        string
		method      string
		body        string
		contentType string
		path        string
		reader      *fake.Reader
		handler     http.Handler
		csrf        bool
		headers     map[string]string
		wantStatus  int
	}{
		{name: "photo libraries", path: "/api/v1/photos/libraries", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo timeline", path: "/api/v1/photos/libraries/" + photoLibraryID + "/timeline?limit=2", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo timeline month", path: "/api/v1/photos/libraries/" + photoLibraryID + "/timeline?month=2026-10", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo timeline bad month", path: "/api/v1/photos/libraries/" + photoLibraryID + "/timeline?month=2026-13", handler: photoHandler, wantStatus: http.StatusBadRequest},
		{name: "photo timeline months", path: "/api/v1/photos/libraries/" + photoLibraryID + "/timeline/months", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo labels", path: "/api/v1/photos/labels", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo label cluster", path: "/api/v1/photos/labels/cat/assets?limit=2", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo label cluster missing", path: "/api/v1/photos/labels/no-such-label/assets", handler: photoHandler, wantStatus: http.StatusNotFound},
		{name: "photo tags", path: "/api/v1/photos/tags", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo tag group", path: "/api/v1/photos/tags/" + url.PathEscape("小橘") + "/assets?limit=2", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo entries", path: "/api/v1/photos/libraries/" + photoLibraryID + "/entries", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo directory", method: http.MethodPost, body: `{"name":"Trips"}`, path: "/api/v1/photos/libraries/" + photoLibraryID + "/directories", handler: photoHandler, csrf: true, wantStatus: http.StatusCreated},
		{name: "photo asset", path: "/api/v1/photos/assets/" + photoAssetID, handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo asset missing", path: "/api/v1/photos/assets/photo:missing", handler: photoHandler, wantStatus: http.StatusNotFound},
		{name: "photo thumbnail pending", path: "/api/v1/photos/assets/" + photoAssetID + "/thumbnail", handler: photoHandler, wantStatus: http.StatusNotFound},
		{name: "photo trash", path: "/api/v1/photos/libraries/" + photoLibraryID + "/trash", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo AI status", path: "/api/v1/photos/ai", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo search", path: "/api/v1/photos/search?q=" + url.QueryEscape("海边的猫"), handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo similar", path: "/api/v1/photos/assets/" + photoAssetID + "/similar?limit=2", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo similar missing", path: "/api/v1/photos/assets/photo:missing/similar", handler: photoHandler, wantStatus: http.StatusNotFound},
		{name: "photo image search without local AI", method: http.MethodPost, body: searchImage, contentType: searchImageType, path: "/api/v1/photos/image-searches", handler: photoHandler, csrf: true, wantStatus: http.StatusServiceUnavailable},
		{name: "photo image search expired", path: "/api/v1/photos/image-searches/0123/assets?limit=2", handler: photoHandler, wantStatus: http.StatusNotFound},
		{name: "photo albums", path: "/api/v1/photos/libraries/" + photoLibraryID + "/albums", handler: photoHandler, wantStatus: http.StatusOK},
		{name: "photo album missing", path: "/api/v1/photos/albums/album:missing/assets", handler: photoHandler, wantStatus: http.StatusNotFound},
		{name: "photos unavailable", path: "/api/v1/photos/libraries", handler: withPhotoPrincipal(photosapi.New(nil, nil)), wantStatus: http.StatusServiceUnavailable},
		{name: "media libraries", path: "/api/v1/media/libraries", handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media library", path: "/api/v1/media/libraries/" + movies.library, handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media library missing", path: "/api/v1/media/libraries/library:missing", handler: mediaHandler, wantStatus: http.StatusNotFound},
		{name: "media library overlap", method: http.MethodPost, body: `{"name":"重复","kind":"movies","spaceId":"` + movies.shared.ID + `","folderIds":["` + movies.folder + `"]}`, path: "/api/v1/media/libraries", handler: mediaHandler, csrf: true, wantStatus: http.StatusConflict},
		{name: "media show rename", method: http.MethodPatch, body: `{"name":".hidden"}`, path: "/api/v1/media/shows/" + movies.show, handler: mediaHandler, csrf: true, wantStatus: http.StatusBadRequest},
		{name: "media video file rename", method: http.MethodPatch, body: `{"fileName":"热天午后.txt"}`, path: "/api/v1/media/videos/" + movies.film, handler: mediaHandler, csrf: true, wantStatus: http.StatusBadRequest},
		{name: "media library scan", method: http.MethodPost, path: "/api/v1/media/libraries/" + movies.library + "/scan", handler: mediaHandler, csrf: true, wantStatus: http.StatusAccepted},
		{name: "media library rename", method: http.MethodPatch, body: `{"name":"家庭影院"}`, path: "/api/v1/media/libraries/" + movies.library, handler: mediaHandler, csrf: true, wantStatus: http.StatusOK},
		{name: "media home", path: "/api/v1/media/home", handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media titles", path: "/api/v1/media/titles?category=movie&sort=name&limit=10", handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media search", path: "/api/v1/media/titles?q=" + url.QueryEscape("繁花"), handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media show", path: "/api/v1/media/shows/" + movies.show, handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media show missing", path: "/api/v1/media/shows/show:missing", handler: mediaHandler, wantStatus: http.StatusNotFound},
		{name: "media video", path: mediaVideo, handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media playback", path: mediaVideo + "/playback?caps=mkv,hevc&quality=original", handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media stream without FFmpeg", path: mediaVideo + "/stream?mode=remux&start=0", handler: mediaHandler, wantStatus: http.StatusServiceUnavailable},
		{name: "media subtitle missing", path: mediaVideo + "/subtitles/x9", handler: mediaHandler, wantStatus: http.StatusNotFound},
		{name: "media progress", method: http.MethodPut, body: `{"position":42,"duration":100}`, path: "/api/v1/media/videos/" + movies.episode + "/progress", handler: mediaHandler, csrf: true, wantStatus: http.StatusOK},
		{name: "media watched", method: http.MethodPut, body: `{"watched":true}`, path: "/api/v1/media/titles/" + movies.show + "/watched", handler: mediaHandler, csrf: true, wantStatus: http.StatusNoContent},
		{name: "media favorite", method: http.MethodPut, path: "/api/v1/media/favorites/" + movies.film, handler: mediaHandler, csrf: true, wantStatus: http.StatusNoContent},
		{name: "media favorite missing", method: http.MethodPut, path: "/api/v1/media/favorites/video:missing", handler: mediaHandler, csrf: true, wantStatus: http.StatusNotFound},
		{name: "media favorites", path: "/api/v1/media/favorites", handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media history", path: "/api/v1/media/history?limit=20", handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media history missing", method: http.MethodDelete, path: "/api/v1/media/history/video:missing", handler: mediaHandler, csrf: true, wantStatus: http.StatusNotFound},
		{name: "media collection create", method: http.MethodPost, body: `{"name":"周末片单","items":["` + movies.film + `"]}`, path: "/api/v1/media/collections", handler: mediaHandler, csrf: true, wantStatus: http.StatusCreated},
		{name: "media collection bad name", method: http.MethodPost, body: `{"name":"   "}`, path: "/api/v1/media/collections", handler: mediaHandler, csrf: true, wantStatus: http.StatusBadRequest},
		{name: "media collections", path: "/api/v1/media/collections", handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media collection missing", path: "/api/v1/media/collections/collection:missing", handler: mediaHandler, wantStatus: http.StatusNotFound},
		{name: "media folders", path: "/api/v1/media/folders", handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media folder", path: "/api/v1/media/folders/" + movies.folder + "?path=" + url.QueryEscape("Heat (1995)"), handler: mediaHandler, wantStatus: http.StatusOK},
		{name: "media folder missing", path: "/api/v1/media/folders/file:missing", handler: mediaHandler, wantStatus: http.StatusNotFound},
		{name: "media artwork missing", path: "/api/v1/media/artwork/" + movies.film + "/backdrop", handler: mediaHandler, wantStatus: http.StatusNotFound},
		{name: "media unavailable", path: "/api/v1/media/home", handler: mediaapi.New(nil, nil), wantStatus: http.StatusServiceUnavailable},
		{name: "feedback list", path: "/api/v1/feedback", handler: feedbackHandler, wantStatus: http.StatusOK},
		{name: "feedback create", method: http.MethodPost, body: created, contentType: createdType, path: "/api/v1/feedback", handler: feedbackHandler, csrf: true, wantStatus: http.StatusCreated},
		{name: "feedback svg", method: http.MethodPost, body: svg, contentType: svgType, path: "/api/v1/feedback", handler: feedbackHandler, csrf: true, wantStatus: http.StatusUnsupportedMediaType},
		{name: "feedback edit", method: http.MethodPut, body: edit, contentType: editType, path: feedbackPath, handler: feedbackHandler, csrf: true, wantStatus: http.StatusOK},
		{name: "feedback stale edit", method: http.MethodPut, body: stale, contentType: staleType, path: feedbackPath, handler: feedbackHandler, csrf: true, wantStatus: http.StatusConflict},
		{name: "feedback resolve", method: http.MethodPatch, body: `{"resolved":true}`, path: feedbackPath, handler: feedbackHandler, csrf: true, wantStatus: http.StatusOK},
		{name: "feedback image", path: feedbackPath + "/images/" + feedbackItem.Images[0].ID, handler: feedbackHandler, wantStatus: http.StatusOK},
		{name: "feedback image missing", path: feedbackPath + "/images/feedback-image:missing", handler: feedbackHandler, wantStatus: http.StatusNotFound},
		{name: "feedback missing", method: http.MethodDelete, path: "/api/v1/feedback/feedback:missing", handler: feedbackHandler, csrf: true, wantStatus: http.StatusNotFound},
		{name: "feedback comment", method: http.MethodPost, body: comment, contentType: commentType, path: feedbackPath + "/comments", handler: feedbackHandler, csrf: true, wantStatus: http.StatusCreated},
		{name: "feedback comment on a missing item", method: http.MethodPost, body: comment, contentType: commentType, path: "/api/v1/feedback/feedback:missing/comments", handler: feedbackHandler, csrf: true, wantStatus: http.StatusNotFound},
		{name: "feedback comment edit", method: http.MethodPut, body: commentEdit, contentType: commentEditType, path: replyPath, handler: feedbackHandler, csrf: true, wantStatus: http.StatusOK},
		{name: "feedback comment stale edit", method: http.MethodPut, body: commentStale, contentType: commentStaleType, path: replyPath, handler: feedbackHandler, csrf: true, wantStatus: http.StatusConflict},
		{name: "feedback comment image", path: feedbackPath + "/images/" + reply.Images[0].ID, handler: feedbackHandler, wantStatus: http.StatusOK},
		{name: "feedback comment missing", method: http.MethodDelete, path: feedbackPath + "/comments/feedback-comment:missing", handler: feedbackHandler, csrf: true, wantStatus: http.StatusNotFound},
		{name: "feedback comment delete", method: http.MethodDelete, path: replyPath, handler: feedbackHandler, csrf: true, wantStatus: http.StatusOK},
		{name: "feedback unavailable", path: "/api/v1/feedback", handler: feedbackapi.New(nil, nil), wantStatus: http.StatusServiceUnavailable},
		{name: "transfers", path: "/api/v1/transfers", handler: transfer.handler, wantStatus: http.StatusOK},
		{name: "transfer", path: "/api/v1/transfers/" + transfer.upload, handler: transfer.handler, wantStatus: http.StatusOK},
		{name: "transfer missing", path: "/api/v1/transfers/upload-missing", handler: transfer.handler, wantStatus: http.StatusNotFound},
		{name: "download create", method: http.MethodPost, body: `{"entryIds":["` + transfer.file + `"]}`, path: "/api/v1/transfers/downloads", handler: transfer.handler, csrf: true, wantStatus: http.StatusCreated},
		{name: "download empty", method: http.MethodPost, body: `{"entryIds":[]}`, path: "/api/v1/transfers/downloads", handler: transfer.handler, csrf: true, wantStatus: http.StatusUnprocessableEntity},
		{name: "download content", path: "/api/v1/transfers/" + transfer.download + "/content", handler: transfer.handler, wantStatus: http.StatusOK},
		{name: "download range", path: "/api/v1/transfers/" + transfer.download + "/content", headers: map[string]string{"Range": "bytes=2-"}, handler: transfer.handler, wantStatus: http.StatusPartialContent},
		{name: "download pause", method: http.MethodPost, path: "/api/v1/transfers/" + transfer.download + "/pause", handler: transfer.handler, csrf: true, wantStatus: http.StatusConflict},
		{name: "upload offset", method: http.MethodHead, path: "/api/v1/uploads/" + transfer.upload, headers: tusHeaders(map[string]string{}), handler: transfer.handler, wantStatus: http.StatusOK},
		{name: "upload part", method: http.MethodPatch, body: "ab", contentType: "application/offset+octet-stream", path: "/api/v1/uploads/" + transfer.upload, headers: tusHeaders(map[string]string{"Upload-Offset": "0"}), handler: transfer.handler, csrf: true, wantStatus: http.StatusNoContent},
		{name: "upload pause", method: http.MethodPost, path: "/api/v1/transfers/" + transfer.upload + "/pause", handler: transfer.handler, csrf: true, wantStatus: http.StatusOK},
		{name: "upload create", method: http.MethodPost, path: "/api/v1/uploads", headers: tusHeaders(map[string]string{"Upload-Length": "3", "Upload-Metadata": uploadMetadata("new.bin")}), handler: transfer.handler, csrf: true, wantStatus: http.StatusCreated},
		{name: "upload create taken", method: http.MethodPost, path: "/api/v1/uploads", headers: tusHeaders(map[string]string{"Upload-Length": "3", "Upload-Metadata": uploadMetadata("clip.bin")}), handler: transfer.handler, csrf: true, wantStatus: http.StatusConflict},
		{name: "upload terminate", method: http.MethodDelete, path: "/api/v1/uploads/" + transfer.other, headers: tusHeaders(map[string]string{}), handler: transfer.handler, csrf: true, wantStatus: http.StatusNoContent},
		{name: "transfer cancel", method: http.MethodPost, path: "/api/v1/transfers/" + transfer.upload + "/cancel", handler: transfer.handler, csrf: true, wantStatus: http.StatusOK},
		{name: "transfer remove", method: http.MethodDelete, path: "/api/v1/transfers/" + transfer.upload, handler: transfer.handler, csrf: true, wantStatus: http.StatusNoContent},
		{name: "transfers clear", method: http.MethodDelete, path: "/api/v1/transfers", handler: transfer.handler, csrf: true, wantStatus: http.StatusNoContent},
		{name: "transfers unavailable", path: "/api/v1/transfers", handler: unavailableTransfers(t), wantStatus: http.StatusServiceUnavailable},
		{name: "health", path: "/healthz", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "system", path: "/api/v1/system", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "disks", path: "/api/v1/disks", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "host state", path: "/api/v1/host-state", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "system unavailable", path: "/api/v1/system", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "disks unavailable", path: "/api/v1/disks", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "host state unavailable", path: "/api/v1/host-state", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "metrics", path: "/api/v1/metrics", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "metrics unavailable", path: "/api/v1/metrics", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "containers", path: "/api/v1/containers", handler: containerAPI(fakecontainers.New()), wantStatus: http.StatusOK},
		{name: "containers disabled", path: "/api/v1/containers", handler: containersapi.New(nil, containersapi.DataSourceLive, nil), wantStatus: http.StatusServiceUnavailable},
		{name: "container logs", path: "/api/v1/containers/" + fakecontainers.ID("jellyfin") + "/logs?tail=2", handler: containerAPI(fakecontainers.New()), wantStatus: http.StatusOK},
		{name: "container action", method: http.MethodPost, body: `{"action":"restart"}`, path: "/api/v1/containers/" + fakecontainers.ID("jellyfin") + "/actions", handler: containerAPI(fakecontainers.New()), wantStatus: http.StatusNoContent},
		{name: "container missing", method: http.MethodPost, body: `{"action":"start"}`, path: "/api/v1/containers/" + strings.Repeat("0", 64) + "/actions", handler: containerAPI(fakecontainers.New()), wantStatus: http.StatusNotFound},
		{name: "apps", path: "/api/v1/apps", handler: appAPI(t), wantStatus: http.StatusOK},
		{name: "app plan", path: "/api/v1/apps/memos/plan", handler: appAPI(t), wantStatus: http.StatusOK},
		{name: "app install stale", method: http.MethodPost, body: `{"digest":"` + strings.Repeat("a", 64) + `"}`, path: "/api/v1/apps/memos/install", handler: appAPI(t), wantStatus: http.StatusConflict},
		{name: "app uninstall missing", method: http.MethodPost, body: `{}`, path: "/api/v1/apps/memos/uninstall", handler: appAPI(t), wantStatus: http.StatusConflict},
		{name: "apps disabled", path: "/api/v1/apps", handler: appstoreapi.New(nil, appstoreapi.DataSourceLive, appstoreapi.Options{}), wantStatus: http.StatusServiceUnavailable},
		{name: "terminal status", path: terminal.StatusPath, handler: terminal.New(terminal.Config{}, nil), wantStatus: http.StatusOK},
		{name: "terminal disabled", path: terminal.SessionPath, handler: terminal.New(terminal.Config{}, nil), wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			newRequest := func() *http.Request {
				method := test.method
				if method == "" {
					method = http.MethodGet
				}
				request := httptest.NewRequest(method, "http://127.0.0.1:8080"+test.path, strings.NewReader(test.body))
				request.AddCookie(&http.Cookie{Name: "anas_session", Value: "contract-test"})
				if test.contentType != "" {
					request.Header.Set("Content-Type", test.contentType)
				} else if test.body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				if test.csrf {
					request.Header.Set("X-CSRF-Token", "contract-test")
				}
				for name, value := range test.headers {
					request.Header.Set(name, value)
				}
				return request
			}
			// The handler consumes the body, so validation gets its own request.
			request := newRequest()
			recorder := httptest.NewRecorder()
			handler := test.handler
			if handler == nil {
				handler = newHandler(test.reader)
			}
			handler.ServeHTTP(recorder, newRequest())

			if got := recorder.Code; got != test.wantStatus {
				t.Fatalf("status = %d, want %d", got, test.wantStatus)
			}
			route, pathParams, err := router.FindRoute(request)
			if err != nil {
				t.Fatalf("find OpenAPI route: %v", err)
			}
			requestInput := &openapi3filter.RequestValidationInput{
				Request:    request,
				PathParams: pathParams,
				Route:      route,
				Options: &openapi3filter.Options{AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error {
					return nil
				}},
			}
			if err := openapi3filter.ValidateRequest(ctx, requestInput); err != nil {
				t.Fatalf("validate request: %v", err)
			}
			responseInput := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: requestInput,
				Status:                 recorder.Code,
				Header:                 recorder.Header(),
			}
			responseInput.SetBodyBytes(recorder.Body.Bytes())
			if err := openapi3filter.ValidateResponse(ctx, responseInput); err != nil {
				t.Fatalf("validate response: %v", err)
			}
		})
	}
}

// feedbackAPI returns the feedback API acting for one member, and an item
// with one screenshot and a comment that has one too.
func feedbackAPI(t *testing.T) (http.Handler, feedback.Item) {
	t.Helper()
	store := openFeedback(t)
	actor := feedback.Actor{ID: "user:contract", Username: "contract"}
	item, err := store.Create(context.Background(), actor, feedback.Content{Text: "截图里的按钮错位", New: [][]byte{feedbackPNG(t)}})
	if err != nil {
		t.Fatalf("create feedback: %v", err)
	}
	item, err = store.AddComment(context.Background(), actor, item.ID, feedback.Content{Text: "附上我这边的截图", New: [][]byte{feedbackPNG(t)}})
	if err != nil {
		t.Fatalf("comment on feedback: %v", err)
	}
	api := feedbackapi.New(store, nil)
	member := accounts.User{ID: "user:contract", Username: "contract", Role: accounts.RoleMember}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.ServeHTTP(w, r.WithContext(feedbackapi.WithUser(r.Context(), member)))
	}), item
}

// transferContract is the transfer API acting for an administrator, with a
// file in the shared space, a download of it and two uploads in progress.
type transferContract struct {
	handler                              http.Handler
	space, file, download, upload, other string
}

func transfersAPI(t *testing.T) transferContract {
	t.Helper()
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	var contract transferContract
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindShared {
			contract.space = space.ID
		}
	}
	fileStore, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	fileService := files.NewService(fileStore, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	entry, err := fileService.Upload(ctx, admin, contract.space, "", "clip.bin", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	contract.file = entry.ID
	service, err := transfers.Open(filepath.Join(t.TempDir(), "transfers.db"), fileService, accountService, transfers.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	download, err := service.CreateDownload(ctx, admin, []string{entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	contract.download = download.ID
	for name, target := range map[string]*string{"first.bin": &contract.upload, "second.bin": &contract.other} {
		upload, err := service.BeginUpload(ctx, admin, transfers.UploadRequest{UploadRequest: files.UploadRequest{
			SpaceID: contract.space, Name: name, Size: 4, Conflict: files.ConflictAsk,
		}})
		if err != nil {
			t.Fatal(err)
		}
		*target = upload.ID
	}
	api, err := transfersapi.New(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	contract.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.ServeHTTP(w, r.WithContext(transfersapi.WithUser(r.Context(), admin)))
	})
	return contract
}

func unavailableTransfers(t *testing.T) http.Handler {
	t.Helper()
	api, err := transfersapi.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func containerAPI(manager *fakecontainers.Manager) http.Handler {
	return containersapi.New(manager, containersapi.DataSourceSimulated, nil)
}

// photoAPI returns the photo API acting for one member, that member's private
// library and an asset in it whose thumbnail is still pending.
func photoAPI(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("open photos: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	member := photos.Principal{UserID: "user:contract"}
	libraries, err := service.Libraries(context.Background(), member)
	if err != nil {
		t.Fatalf("list photo libraries: %v", err)
	}
	var private string
	for _, library := range libraries {
		if library.Kind == photos.LibraryKindPrivate {
			private = library.ID
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	asset, err := service.Import(context.Background(), member, photos.ImportRequest{LibraryID: private, Name: "p.png", Content: &encoded})
	if err != nil {
		t.Fatalf("import photo: %v", err)
	}
	return withPhotoPrincipal(photosapi.New(service, nil)), private, asset.ID
}

// imageSearchForm is a multipart body with an image to search by.
func imageSearchForm(t *testing.T) (string, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "query.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(part, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), writer.FormDataContentType()
}

func withPhotoPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(photosapi.WithPrincipal(r.Context(), photos.Principal{UserID: "user:contract"})))
	})
}

func appAPI(t *testing.T) http.Handler {
	store, err := fakeappstore.New(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return appstoreapi.New(store, appstoreapi.DataSourceSimulated, appstoreapi.Options{Host: appstoreapi.DevelopmentHost{}})
}
