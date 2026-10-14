package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/feedback"
	"github.com/zhongwater123/A-NAS/internal/feedbackapi"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

// feedbackForm encodes a feedback form; images are PNG unless raw bytes are given.
type feedbackForm struct {
	text    string
	version int64
	keep    []string
	images  [][]byte
}

func (f feedbackForm) encode(t *testing.T) (string, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("text", f.text)
	if f.version > 0 {
		_ = writer.WriteField("version", strconv.FormatInt(f.version, 10))
	}
	for _, id := range f.keep {
		_ = writer.WriteField("keep", id)
	}
	for index, data := range f.images {
		part, err := writer.CreateFormFile("image", "screenshot-"+strconv.Itoa(index)+".png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(data)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), writer.FormDataContentType()
}

func feedbackPNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func openFeedback(t *testing.T) *feedback.Store {
	t.Helper()
	store, err := feedback.Open(filepath.Join(t.TempDir(), "feedback.db"), feedback.Options{})
	if err != nil {
		t.Fatalf("open feedback: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// feedbackRequest sends a request as a signed-in account; csrf adds the
// session's token.
func feedbackRequest(account signedIn, method, path, contentType, body string, csrf bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(account.cookie)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", account.csrf)
	}
	recorder := httptest.NewRecorder()
	account.handler.ServeHTTP(recorder, request)
	return recorder
}

func sendFeedbackForm(t *testing.T, account signedIn, method, path string, form feedbackForm) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := form.encode(t)
	return feedbackRequest(account, method, path, contentType, body, true)
}

func decodeItem(t *testing.T, recorder *httptest.ResponseRecorder) feedback.Item {
	t.Helper()
	var item feedback.Item
	if err := json.Unmarshal(recorder.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode item %s: %v", recorder.Body.String(), err)
	}
	return item
}

func TestFeedbackIsOneListEverySignedInAccountEdits(t *testing.T) {
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
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService,
		Feedback: feedbackapi.New(openFeedback(t), nil), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/feedback", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d", anonymous.Code)
	}
	owner := signIn(t, handler, "owner", "correct horse battery staple")
	member := signIn(t, handler, "member", "member horse battery staple")

	body, contentType := feedbackForm{text: "无 CSRF"}.encode(t)
	if forged := feedbackRequest(member, http.MethodPost, "/api/v1/feedback", contentType, body, false); forged.Code != http.StatusForbidden {
		t.Fatalf("create without CSRF = %d", forged.Code)
	}
	created := sendFeedbackForm(t, member, http.MethodPost, "/api/v1/feedback", feedbackForm{text: "上传 2 GB 文件时卡住", images: [][]byte{feedbackPNG(t)}})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	item := decodeItem(t, created)
	if item.CreatedBy != "member" || len(item.Images) != 1 {
		t.Fatalf("created = %+v", item)
	}

	// The administrator sees the member's item and its screenshot.
	listed := feedbackRequest(owner, http.MethodGet, "/api/v1/feedback", "", "", false)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), item.ID) {
		t.Fatalf("owner list = %d %s", listed.Code, listed.Body.String())
	}
	picture := feedbackRequest(owner, http.MethodGet, "/api/v1/feedback/"+item.ID+"/images/"+item.Images[0].ID, "", "", false)
	if picture.Code != http.StatusOK || picture.Header().Get("Content-Type") != "image/png" ||
		picture.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(picture.Body.Bytes(), feedbackPNG(t)) {
		t.Fatalf("image = %d %v", picture.Code, picture.Header())
	}

	// The administrator rewrites it and drops the screenshot; the member's
	// copy is now stale.
	edited := sendFeedbackForm(t, owner, http.MethodPut, "/api/v1/feedback/"+item.ID, feedbackForm{text: "上传大文件时进度停在 99%", version: item.Version})
	if edited.Code != http.StatusOK {
		t.Fatalf("edit = %d %s", edited.Code, edited.Body.String())
	}
	if changed := decodeItem(t, edited); changed.UpdatedBy != "owner" || len(changed.Images) != 0 {
		t.Fatalf("edited = %+v", changed)
	}
	if stale := sendFeedbackForm(t, member, http.MethodPut, "/api/v1/feedback/"+item.ID, feedbackForm{text: "旧", version: item.Version}); stale.Code != http.StatusConflict ||
		!strings.Contains(stale.Body.String(), "feedback_changed") {
		t.Fatalf("stale edit = %d %s", stale.Code, stale.Body.String())
	}

	resolved := feedbackRequest(member, http.MethodPatch, "/api/v1/feedback/"+item.ID, "application/json", `{"resolved":true}`, true)
	if resolved.Code != http.StatusOK || decodeItem(t, resolved).ResolvedBy != "member" {
		t.Fatalf("resolve = %d %s", resolved.Code, resolved.Body.String())
	}
	if bad := feedbackRequest(member, http.MethodPatch, "/api/v1/feedback/"+item.ID, "application/json", `{}`, true); bad.Code != http.StatusBadRequest {
		t.Fatalf("resolve without a state = %d", bad.Code)
	}

	svg := sendFeedbackForm(t, member, http.MethodPost, "/api/v1/feedback", feedbackForm{images: [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)}})
	if svg.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("svg = %d %s", svg.Code, svg.Body.String())
	}

	// Comments: a member replies with a screenshot, the administrator rewrites
	// that reply and then deletes it; writes need the CSRF token.
	commentBody, commentType := feedbackForm{text: "我这边也能复现", images: [][]byte{feedbackPNG(t)}}.encode(t)
	if forged := feedbackRequest(member, http.MethodPost, "/api/v1/feedback/"+item.ID+"/comments", commentType, commentBody, false); forged.Code != http.StatusForbidden {
		t.Fatalf("comment without CSRF = %d", forged.Code)
	}
	commented := sendFeedbackForm(t, member, http.MethodPost, "/api/v1/feedback/"+item.ID+"/comments", feedbackForm{text: "我这边也能复现", images: [][]byte{feedbackPNG(t)}})
	if commented.Code != http.StatusCreated {
		t.Fatalf("comment = %d %s", commented.Code, commented.Body.String())
	}
	reply := decodeItem(t, commented).Comments[0]
	if reply.CreatedBy != "member" || len(reply.Images) != 1 {
		t.Fatalf("reply = %+v", reply)
	}
	if picture := feedbackRequest(owner, http.MethodGet, "/api/v1/feedback/"+item.ID+"/images/"+reply.Images[0].ID, "", "", false); picture.Code != http.StatusOK || picture.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("comment image = %d %v", picture.Code, picture.Header())
	}
	rewritten := sendFeedbackForm(t, owner, http.MethodPut, "/api/v1/feedback/"+item.ID+"/comments/"+reply.ID, feedbackForm{text: "已在 1.0.2 修复", version: reply.Version})
	if rewritten.Code != http.StatusOK || decodeItem(t, rewritten).Comments[0].UpdatedBy != "owner" {
		t.Fatalf("edit comment = %d %s", rewritten.Code, rewritten.Body.String())
	}
	if stale := sendFeedbackForm(t, member, http.MethodPut, "/api/v1/feedback/"+item.ID+"/comments/"+reply.ID, feedbackForm{text: "旧", version: reply.Version}); stale.Code != http.StatusConflict ||
		!strings.Contains(stale.Body.String(), "comment_changed") {
		t.Fatalf("stale comment edit = %d %s", stale.Code, stale.Body.String())
	}
	if removed := feedbackRequest(owner, http.MethodDelete, "/api/v1/feedback/"+item.ID+"/comments/"+reply.ID, "", "", true); removed.Code != http.StatusOK || len(decodeItem(t, removed).Comments) != 0 {
		t.Fatalf("delete comment = %d %s", removed.Code, removed.Body.String())
	}
	if missing := feedbackRequest(owner, http.MethodDelete, "/api/v1/feedback/"+item.ID+"/comments/"+reply.ID, "", "", true); missing.Code != http.StatusNotFound ||
		!strings.Contains(missing.Body.String(), "comment_not_found") {
		t.Fatalf("delete comment twice = %d %s", missing.Code, missing.Body.String())
	}

	if deleted := feedbackRequest(owner, http.MethodDelete, "/api/v1/feedback/"+item.ID, "", "", true); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	if listed := feedbackRequest(member, http.MethodGet, "/api/v1/feedback", "", "", false); strings.Contains(listed.Body.String(), item.ID) {
		t.Fatalf("list after delete = %s", listed.Body.String())
	}
}
