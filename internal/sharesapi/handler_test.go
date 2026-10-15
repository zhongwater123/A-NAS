package sharesapi

import (
	"context"
	"encoding/json"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type backend struct{}

func (backend) Capture(ctx context.Context, u shares.User, ids []string, visit func(shares.Entry, io.Reader) error) error {
	return visit(shares.Entry{Path: "hello.txt", Size: 5}, strings.NewReader("hello"))
}
func (backend) Save(context.Context, shares.User, shares.Target, []shares.Entry, func(string) (io.ReadCloser, error), func(string, func() error) error, func(int64)) error {
	return nil
}
func fixture(t *testing.T) (*shares.Store, http.Handler, *atomic.Int64) {
	t.Helper()
	dir := t.TempDir()
	clock := new(atomic.Int64)
	clock.Store(time.Now().Unix())
	store, err := shares.Open(filepath.Join(dir, "db"), filepath.Join(dir, "content"), shares.Options{Backend: backend{}, Now: func() time.Time { return time.Unix(clock.Load(), 0) }, Active: func(context.Context, string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, New(store, nil, func(context.Context) ([]accounts.Profile, error) {
		return []accounts.Profile{{ID: "a", Username: "Alice"}, {ID: "b", Username: "Bob"}, {ID: "c", Username: "Carol"}}, nil
	}), clock
}
func request(h http.Handler, user, method, url, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	if user != "" {
		r = r.WithContext(WithUser(r.Context(), accounts.User{ID: user, Username: user}))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func published(t *testing.T, s *shares.Store) shares.Share {
	t.Helper()
	v, err := s.Create(context.Background(), shares.User{ID: "a", Username: "Alice"}, shares.CreateRequest{RequestID: "create", SourceIDs: []string{"src"}, RecipientIDs: []string{"b", "c"}, Message: "你好 👩🏽‍💻"})
	if err != nil {
		t.Fatal(err)
	}
	for range 500 {
		v, err = s.Get(context.Background(), shares.User{ID: "a"}, v.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == "published" {
			return v
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("prepare timeout")
	return v
}
func TestAuthenticationAndMemberDirectory(t *testing.T) {
	_, h, _ := fixture(t)
	if w := request(h, "", "GET", PathPrefix, ""); w.Code != 401 {
		t.Fatalf("unauthenticated %d", w.Code)
	}
	w := request(h, "a", "GET", PathPrefix+"/members", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"id":"a"`) {
		t.Fatalf("directory %d %s", w.Code, w.Body.String())
	}
	if w = request(h, "a", "POST", PathPrefix, `{"requestId":"bad","sourceIds":["src"],"recipientIds":["b"],"sender":{"id":"admin"}}`); w.Code != 400 {
		t.Fatalf("forged actor accepted %d", w.Code)
	}
}
func TestSaveJobListIsScopedAndHidesTargets(t *testing.T) {
	s, h, _ := fixture(t)
	v := published(t, s)
	base := PathPrefix + "/" + v.ID
	body := `{"requestId":"save1","entryIds":["` + v.Entries[0].ID + `"],"target":{"rootId":"home","parentId":"private-target"}}`
	w := request(h, "b", "POST", base+"/save", body)
	if w.Code != 202 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	var job shares.Job
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w = request(h, "b", "GET", base+"/jobs", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), job.ID) || strings.Contains(w.Body.String(), "private-target") {
		t.Fatalf("own jobs %d %s", w.Code, w.Body.String())
	}
	w = request(h, "c", "GET", base+"/jobs", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), job.ID) {
		t.Fatalf("other recipient jobs %d %s", w.Code, w.Body.String())
	}
	if w = request(h, "a", "GET", PathPrefix+"/jobs/"+job.ID, ""); w.Code != 404 {
		t.Fatalf("sender sees job %d", w.Code)
	}
	w = request(h, "b", "GET", PathPrefix+"/jobs/"+job.ID, "")
	if w.Code != 200 {
		t.Fatalf("job status %d", w.Code)
	}
}
func TestPrivateThreadsViewedStateAndRangeExpiry(t *testing.T) {
	s, h, clock := fixture(t)
	v := published(t, s)
	base := PathPrefix + "/" + v.ID
	if w := request(h, "admin", "GET", base, ""); w.Code != 404 {
		t.Fatalf("admin %d", w.Code)
	}
	w := request(h, "b", "GET", base, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var own shares.Share
	if err := json.Unmarshal(w.Body.Bytes(), &own); err != nil {
		t.Fatal(err)
	}
	if len(own.Recipients) != 1 || own.Recipients[0].ViewedAt != nil {
		t.Fatalf("prefetch viewed %+v", own)
	}
	if w = request(h, "b", "POST", base+"/view", ""); w.Code != 200 {
		t.Fatalf("view %d", w.Code)
	}
	if w = request(h, "b", "GET", base+"/messages?recipientId=c", ""); w.Code != 404 {
		t.Fatalf("other thread %d", w.Code)
	}
	if w = request(h, "b", "POST", base+"/messages", `{"recipientId":"b","requestId":"reply","text":"👍🏽"}`); w.Code != 201 {
		t.Fatalf("message %d %s", w.Code, w.Body.String())
	}
	url := base + "/entries/" + v.Entries[0].ID + "/content"
	r := httptest.NewRequest("GET", url, nil)
	r.Header.Set("Range", "bytes=1-3")
	r = r.WithContext(WithUser(r.Context(), accounts.User{ID: "b"}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "ell" {
		t.Fatalf("range %d %q", w.Code, w.Body.String())
	}
	clock.Add(int64(7 * 24 * time.Hour / time.Second))
	if w = request(h, "b", "GET", url, ""); w.Code != 410 {
		t.Fatalf("expired %d", w.Code)
	}
}
