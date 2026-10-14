package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/transfers"
	"github.com/zhongwater123/A-NAS/internal/transfersapi"
)

type transferFixture struct {
	handler http.Handler
	files   *files.Service
	shared  accounts.Space
	owner   signedIn
	member  signedIn
	volume  *apiVolume
}

// apiVolume stands in for the data volume's check; tests take it offline.
type apiVolume struct{ offline atomic.Bool }

func (v *apiVolume) Check(context.Context, string, bool) error {
	if v.offline.Load() {
		return files.ErrVolumeUnavailable
	}
	return nil
}

func newTransferFixture(t *testing.T) *transferFixture {
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
	if _, err := accountService.CreateMember(ctx, admin, "member", "member horse battery staple"); err != nil {
		t.Fatal(err)
	}
	fileStore, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	volume := &apiVolume{}
	fileService := files.NewService(fileStore, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{DisableCapacityReserve: true, VolumeGuard: volume})
	service, err := transfers.Open(filepath.Join(t.TempDir(), "transfers.db"), fileService, accountService, transfers.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	api, err := transfersapi.New(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService, Files: fileService,
		Transfers: api, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	f := &transferFixture{handler: handler, files: fileService, volume: volume}
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindShared {
			f.shared = space
		}
	}
	f.owner = signIn(t, handler, "owner", "correct horse battery staple")
	f.member = signIn(t, handler, "member", "member horse battery staple")
	return f
}

// tus sends a tus 1.0 request as a signed-in account.
func tus(account signedIn, method, path string, headers map[string]string, body string, csrf bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(account.cookie)
	request.Header.Set("Tus-Resumable", "1.0.0")
	if csrf {
		request.Header.Set("X-CSRF-Token", account.csrf)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	account.handler.ServeHTTP(recorder, request)
	return recorder
}

func tusMetadata(values map[string]string) string {
	var pairs []string
	for key, value := range values {
		pairs = append(pairs, key+" "+base64.StdEncoding.EncodeToString([]byte(value)))
	}
	return strings.Join(pairs, ",")
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", recorder.Body.String(), err)
	}
	return body.Error.Code
}

func TestAnUploadSavedWhileTheVolumeIsAwayIsSavedWhenAskedAgain(t *testing.T) {
	f := newTransferFixture(t)
	created := tus(f.member, http.MethodPost, "/api/v1/uploads", map[string]string{
		"Upload-Length":   "6",
		"Upload-Metadata": tusMetadata(map[string]string{"filename": "整季.mkv", "spaceId": f.shared.ID}),
	}, "", true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create upload = %d %s", created.Code, created.Body.String())
	}
	location := created.Header().Get("Location")
	path := location[strings.Index(location, "/api/v1/uploads/"):]
	// The last part arrives just as the volume goes away.
	f.volume.offline.Store(true)
	last := tus(f.member, http.MethodPatch, path, map[string]string{"Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"}, "abcdef", true)
	if last.Code != http.StatusLocked || errorCode(t, last) != "volume_unavailable" {
		t.Fatalf("last part with the volume away = %d %s", last.Code, last.Body.String())
	}
	// Asking where to go on from does not take the upload for complete.
	if head := tus(f.member, http.MethodHead, path, nil, "", false); head.Code != http.StatusLocked {
		t.Fatalf("HEAD with the volume away = %d offset %s", head.Code, head.Header().Get("Upload-Offset"))
	}
	f.volume.offline.Store(false)
	if head := tus(f.member, http.MethodHead, path, nil, "", false); head.Code != http.StatusOK || head.Header().Get("Upload-Offset") != "6" {
		t.Fatalf("HEAD with the volume back = %d offset %s %s", head.Code, head.Header().Get("Upload-Offset"), head.Body.String())
	}
	list := f.member.call(t, http.MethodGet, "/api/v1/transfers", "")
	if !strings.Contains(list.Body.String(), `"state":"completed"`) {
		t.Fatalf("transfer list after saving = %s", list.Body.String())
	}
}

func TestUploadsResumeOverTusAndShowInTheTransferList(t *testing.T) {
	f := newTransferFixture(t)
	create := func(account signedIn, name string, length int, csrf bool) *httptest.ResponseRecorder {
		return tus(account, http.MethodPost, "/api/v1/uploads", map[string]string{
			"Upload-Length":   strconv.Itoa(length),
			"Upload-Metadata": tusMetadata(map[string]string{"filename": name, "spaceId": f.shared.ID, "relativePath": "照片/2026", "batch": "b1", "lastModified": "1760000000000"}),
		}, "", csrf)
	}
	if forged := create(f.member, "a.jpg", 6, false); forged.Code != http.StatusForbidden {
		t.Fatalf("upload without CSRF = %d", forged.Code)
	}
	anonymous := httptest.NewRecorder()
	f.handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/api/v1/uploads", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous upload = %d", anonymous.Code)
	}
	created := create(f.member, "a.jpg", 6, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create upload = %d %s", created.Code, created.Body.String())
	}
	location := created.Header().Get("Location")
	path := location[strings.Index(location, "/api/v1/uploads/"):]
	id := path[len("/api/v1/uploads/"):]

	patch := func(account signedIn, offset int, body string) *httptest.ResponseRecorder {
		return tus(account, http.MethodPatch, path, map[string]string{"Upload-Offset": strconv.Itoa(offset), "Content-Type": "application/offset+octet-stream"}, body, true)
	}
	if first := patch(f.member, 0, "abc"); first.Code != http.StatusNoContent || first.Header().Get("Upload-Offset") != "3" {
		t.Fatalf("first part = %d offset %s %s", first.Code, first.Header().Get("Upload-Offset"), first.Body.String())
	}
	if other := tus(f.owner, http.MethodHead, path, nil, "", false); other.Code != http.StatusNotFound {
		t.Fatalf("another account found the upload: %d", other.Code)
	}
	if head := tus(f.member, http.MethodHead, path, nil, "", false); head.Code != http.StatusOK || head.Header().Get("Upload-Offset") != "3" || head.Header().Get("Upload-Length") != "6" {
		t.Fatalf("resume HEAD = %d offset %s length %s", head.Code, head.Header().Get("Upload-Offset"), head.Header().Get("Upload-Length"))
	}
	if last := patch(f.member, 3, "def"); last.Code != http.StatusNoContent {
		t.Fatalf("last part = %d %s", last.Code, last.Body.String())
	}
	// A browser that missed the final response sees it complete.
	if head := tus(f.member, http.MethodHead, path, nil, "", false); head.Header().Get("Upload-Offset") != "6" {
		t.Fatalf("HEAD after completion offset = %s", head.Header().Get("Upload-Offset"))
	}

	list := f.member.call(t, http.MethodGet, "/api/v1/transfers", "")
	var listed struct {
		Items []transfers.Transfer `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("transfer list = %s, %v", list.Body.String(), err)
	}
	upload := listed.Items[0]
	if upload.ID != id || upload.State != transfers.StateCompleted || upload.RelativePath != "照片/2026" || upload.Batch != "b1" ||
		len(upload.Location.Folders) != 2 || upload.Location.Folders[1].Name != "2026" {
		t.Fatalf("listed upload = %#v", upload)
	}
	if others := f.owner.call(t, http.MethodGet, "/api/v1/transfers", ""); strings.Contains(others.Body.String(), id) {
		t.Fatalf("another account lists the upload: %s", others.Body.String())
	}

	// Asking first: a taken name is refused before any byte is sent.
	if taken := tus(f.member, http.MethodPost, "/api/v1/uploads", map[string]string{
		"Upload-Length":   "1",
		"Upload-Metadata": tusMetadata(map[string]string{"filename": "a.jpg", "spaceId": f.shared.ID, "relativePath": "照片/2026"}),
	}, "", true); taken.Code != http.StatusConflict || errorCode(t, taken) != "name_conflict" {
		t.Fatalf("taken name = %d %s", taken.Code, taken.Body.String())
	}
	// An empty file is saved as soon as it is created.
	if empty := tus(f.member, http.MethodPost, "/api/v1/uploads", map[string]string{
		"Upload-Length":   "0",
		"Upload-Metadata": tusMetadata(map[string]string{"filename": "a.jpg", "spaceId": f.shared.ID, "relativePath": "照片/2026", "conflict": "rename"}),
	}, "", true); empty.Code != http.StatusCreated {
		t.Fatalf("empty upload = %d %s", empty.Code, empty.Body.String())
	}
	if invalid := tus(f.member, http.MethodPost, "/api/v1/uploads", map[string]string{
		"Upload-Length":   "1",
		"Upload-Metadata": tusMetadata(map[string]string{"filename": "../escape", "spaceId": f.shared.ID}),
	}, "", true); invalid.Code != http.StatusUnprocessableEntity || errorCode(t, invalid) != "validation_failed" {
		t.Fatalf("invalid name = %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestUploadsCanBePausedAndCanceledFromAnyBrowser(t *testing.T) {
	f := newTransferFixture(t)
	created := tus(f.member, http.MethodPost, "/api/v1/uploads", map[string]string{
		"Upload-Length":   "10",
		"Upload-Metadata": tusMetadata(map[string]string{"filename": "big.iso", "spaceId": f.shared.ID}),
	}, "", true)
	location := created.Header().Get("Location")
	id := location[strings.LastIndex(location, "/")+1:]
	if paused := f.member.call(t, http.MethodPost, "/api/v1/transfers/"+id+"/pause", ""); paused.Code != http.StatusOK || !strings.Contains(paused.Body.String(), `"state":"paused"`) {
		t.Fatalf("pause = %d %s", paused.Code, paused.Body.String())
	}
	if running := f.member.call(t, http.MethodDelete, "/api/v1/transfers/"+id, ""); running.Code != http.StatusConflict {
		t.Fatalf("removing an unfinished transfer = %d", running.Code)
	}
	if other := f.owner.call(t, http.MethodPost, "/api/v1/transfers/"+id+"/cancel", ""); other.Code != http.StatusNotFound {
		t.Fatalf("another account canceled the upload: %d", other.Code)
	}
	if canceled := f.member.call(t, http.MethodPost, "/api/v1/transfers/"+id+"/cancel", ""); canceled.Code != http.StatusOK || !strings.Contains(canceled.Body.String(), `"state":"canceled"`) {
		t.Fatalf("cancel = %d %s", canceled.Code, canceled.Body.String())
	}
	if gone := tus(f.member, http.MethodHead, "/api/v1/uploads/"+id, nil, "", false); gone.Code != http.StatusNotFound {
		t.Fatalf("canceled upload HEAD = %d", gone.Code)
	}
	if cleared := f.member.call(t, http.MethodDelete, "/api/v1/transfers", ""); cleared.Code != http.StatusNoContent {
		t.Fatalf("clear = %d", cleared.Code)
	}
}

func TestDownloadsAreFollowedThroughTheTransferAPI(t *testing.T) {
	f := newTransferFixture(t)
	owner, err := accountsUser(f, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx := accounts.WithSessionToken(context.Background(), f.owner.cookie.Value)
	entry, err := f.files.Upload(ctx, owner, f.shared.ID, "", "片段.mp4", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	if forged := feedbackRequest(f.member, http.MethodPost, "/api/v1/transfers/downloads", "application/json", `{"entryIds":["`+entry.ID+`"]}`, false); forged.Code != http.StatusForbidden {
		t.Fatalf("download without CSRF = %d", forged.Code)
	}
	created := f.member.call(t, http.MethodPost, "/api/v1/transfers/downloads", `{"entryIds":["`+entry.ID+`"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create download = %d %s", created.Code, created.Body.String())
	}
	var download transfers.Transfer
	if err := json.Unmarshal(created.Body.Bytes(), &download); err != nil || download.ContentURL == "" || download.State != transfers.StatePending {
		t.Fatalf("download = %#v, %v", download, err)
	}
	if other := f.owner.call(t, http.MethodGet, download.ContentURL, ""); other.Code != http.StatusNotFound {
		t.Fatalf("another account fetched the download: %d", other.Code)
	}
	content := f.member.call(t, http.MethodGet, download.ContentURL, "")
	if content.Code != http.StatusOK || content.Body.String() != "0123456789" || !strings.HasPrefix(content.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("content = %d %q %s", content.Code, content.Body.String(), content.Header().Get("Content-Disposition"))
	}
	got := f.member.call(t, http.MethodGet, "/api/v1/transfers/"+download.ID, "")
	if !strings.Contains(got.Body.String(), `"state":"completed"`) {
		t.Fatalf("download after content = %s", got.Body.String())
	}
	if missing := f.member.call(t, http.MethodPost, "/api/v1/transfers/downloads", `{"entryIds":[]}`); missing.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty selection = %d", missing.Code)
	}
}

func accountsUser(f *transferFixture, username string) (accounts.User, error) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	account := f.owner
	if username != "owner" {
		account = f.member
	}
	request.AddCookie(account.cookie)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	var session struct {
		User accounts.User `json:"user"`
	}
	err := json.Unmarshal(recorder.Body.Bytes(), &session)
	return session.User, err
}
