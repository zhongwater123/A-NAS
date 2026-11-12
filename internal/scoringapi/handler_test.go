//go:build linux

package scoringapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/scoring"
	"github.com/zhongwater123/A-NAS/internal/scoringapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// sessions answers like the Host Agent's lookup: token → account.
type sessions map[string]accounts.SessionUser

func (s sessions) ResolveSessionUser(_ context.Context, token string) (accounts.SessionUser, error) {
	user, ok := s[token]
	if !ok {
		return accounts.SessionUser{}, accounts.ErrSessionNotFound
	}
	return user, nil
}

func (s sessions) ActiveProfiles(context.Context) ([]accounts.Profile, error) {
	var profiles []accounts.Profile
	for _, user := range s {
		profiles = append(profiles, accounts.Profile{ID: user.UserID, Username: user.Username})
	}
	return profiles, nil
}

type api struct {
	t       *testing.T
	handler http.Handler
}

func (a api) call(token, method, path string, body any) *httptest.ResponseRecorder {
	a.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set(serviceproxy.SessionHeader, token)
	}
	recorder := httptest.NewRecorder()
	a.handler.ServeHTTP(recorder, request)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("%d %s: %v", recorder.Code, recorder.Body, err)
	}
	return value
}

func setup(t *testing.T) api {
	t.Helper()
	photos := t.TempDir()
	for i := range 4 {
		img := image.NewRGBA(image.Rect(0, 0, 20, 10))
		img.Set(i, 0, color.RGBA{uint8(40 * i), 0, 0, 255})
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, img); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(photos, "第一批", string(rune('a'+i))+".png")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := scoring.Open(t.TempDir(), scoring.Options{PhotosDir: photos, Photos: filesystem.NewRootFileSystem(photos, false)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = store.Close()
	})
	directory := sessions{
		"admin-token": {UserID: "user:admin", Username: "admin", Role: accounts.RoleAdmin},
		"alice-token": {UserID: "user:alice", Username: "alice", Role: accounts.RoleMember},
		"bob-token":   {UserID: "user:bob", Username: "bob", Role: accounts.RoleMember},
		"carol-token": {UserID: "user:carol", Username: "carol", Role: accounts.RoleMember},
	}
	return api{t: t, handler: scoringapi.Start(ctx, store, directory, "test", nil)}
}

type batchList struct {
	Items []struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Rated int    `json:"rated"`
	} `json:"items"`
}

func TestRatersSeeOnlyTheirOwnWorkAndAdministratorsSeeEveryone(t *testing.T) {
	a := setup(t)
	if code := a.call("", http.MethodGet, scoringapi.PathPrefix+"/me", nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", code)
	}
	created := a.call("admin-token", http.MethodPost, scoringapi.AdminPrefix+"/batches",
		map[string]any{"name": "第一批", "source": "第一批", "required": 2, "rubric": "看构图"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	batch := decode[struct{ ID string }](t, created).ID
	deadline := time.Now().Add(10 * time.Second)
	for {
		batches := decode[batchList](t, a.call("admin-token", http.MethodGet, scoringapi.AdminPrefix+"/batches", nil))
		if len(batches.Items) == 1 && batches.Items[0].State == "open" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("import did not finish: %+v", batches)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if me := decode[map[string]bool](t, a.call("alice-token", http.MethodGet, scoringapi.PathPrefix+"/me", nil)); me["rater"] || me["admin"] {
		t.Fatalf("alice before being chosen: %v", me)
	}
	if code := a.call("alice-token", http.MethodGet, scoringapi.PathPrefix+"/batches", nil).Code; code != http.StatusForbidden {
		t.Fatalf("batches for a member not chosen: %d", code)
	}
	if code := a.call("alice-token", http.MethodGet, scoringapi.AdminPrefix+"/batches", nil).Code; code != http.StatusForbidden {
		t.Fatalf("administrators' batches for a member: %d", code)
	}
	if code := a.call("admin-token", http.MethodPut, scoringapi.AdminPrefix+"/raters/user:nobody", nil).Code; code != http.StatusNotFound {
		t.Fatalf("choosing an unknown account: %d", code)
	}
	for _, account := range []string{"user:alice", "user:bob"} {
		if code := a.call("admin-token", http.MethodPut, scoringapi.AdminPrefix+"/raters/"+account, nil).Code; code != http.StatusNoContent {
			t.Fatalf("choosing %s: %d", account, code)
		}
	}

	claims := decode[struct {
		Items []struct{ Token string } `json:"items"`
	}](t, a.call("alice-token", http.MethodPost, scoringapi.PathPrefix+"/batches/"+batch+"/claims", nil))
	if len(claims.Items) != 3 {
		t.Fatalf("alice holds %d photos", len(claims.Items))
	}
	token := claims.Items[0].Token
	image := a.call("alice-token", http.MethodGet, scoringapi.PathPrefix+"/claims/"+token+"/image", nil)
	if image.Code != http.StatusOK || image.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("alice's photo: %d %s", image.Code, image.Header().Get("Content-Type"))
	}
	if code := a.call("bob-token", http.MethodGet, scoringapi.PathPrefix+"/claims/"+token+"/image", nil).Code; code != http.StatusNotFound {
		t.Fatalf("bob opened alice's photo: %d", code)
	}
	rating := map[string]any{"submission": "alice-0001", "kind": "score", "score": 3.47, "viewMs": 3100,
		"viewport": map[string]int{"width": 1440, "height": 900}, "display": map[string]int{"width": 1200, "height": 600}, "pixelRatio": 2}
	if rated := a.call("bob-token", http.MethodPut, scoringapi.PathPrefix+"/claims/"+token+"/rating", rating); rated.Code != http.StatusNotFound {
		t.Fatalf("bob answered alice's photo: %d", rated.Code)
	}
	rated := a.call("alice-token", http.MethodPut, scoringapi.PathPrefix+"/claims/"+token+"/rating", rating)
	if rated.Code != http.StatusOK || decode[map[string]int](t, rated)["rated"] != 1 {
		t.Fatalf("alice's answer: %d %s", rated.Code, rated.Body)
	}

	tooFine := map[string]any{"submission": "alice-0002", "kind": "score", "score": 2.345, "viewMs": 100}
	if code := a.call("alice-token", http.MethodPut, scoringapi.PathPrefix+"/claims/"+claims.Items[1].Token+"/rating", tooFine).Code; code != http.StatusUnprocessableEntity {
		t.Fatalf("a score with three decimals: %d", code)
	}
	recent := decode[struct {
		Items []struct {
			Token string  `json:"token"`
			Score float64 `json:"score"`
		} `json:"items"`
	}](t, a.call("alice-token", http.MethodGet, scoringapi.PathPrefix+"/batches/"+batch+"/recent", nil))
	if len(recent.Items) != 1 || recent.Items[0].Token != token || recent.Items[0].Score != 3.47 {
		t.Fatalf("alice's recent scores: %+v", recent)
	}
	if thumb := a.call("alice-token", http.MethodGet, scoringapi.PathPrefix+"/claims/"+token+"/thumbnail", nil); thumb.Code != http.StatusOK {
		t.Fatalf("alice's thumbnail: %d", thumb.Code)
	}
	if code := a.call("bob-token", http.MethodGet, scoringapi.PathPrefix+"/claims/"+token+"/thumbnail", nil).Code; code != http.StatusNotFound {
		t.Fatalf("bob opened alice's thumbnail: %d", code)
	}
	cover := a.call("admin-token", http.MethodGet, scoringapi.AdminPrefix+"/batches/"+batch+"/cover", nil)
	if cover.Code != http.StatusOK || cover.Header().Get("Content-Type") != "image/jpeg" || cover.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("the batch's cover: %d %v", cover.Code, cover.Header())
	}
	if code := a.call("bob-token", http.MethodGet, scoringapi.AdminPrefix+"/batches/"+batch+"/cover", nil).Code; code != http.StatusForbidden {
		t.Fatalf("a rater opened the batch's cover: %d", code)
	}

	bobs := a.call("bob-token", http.MethodGet, scoringapi.PathPrefix+"/batches", nil)
	if list := decode[batchList](t, bobs); len(list.Items) != 1 || list.Items[0].Rated != 0 {
		t.Fatalf("bob's batches: %s", bobs.Body)
	}
	if strings.Contains(bobs.Body.String(), "alice") || strings.Contains(bobs.Body.String(), `"score"`) {
		t.Fatalf("bob sees something of alice's: %s", bobs.Body)
	}
	stats := a.call("admin-token", http.MethodGet, scoringapi.AdminPrefix+"/batches/"+batch+"/stats", nil)
	if stats.Code != http.StatusOK || !strings.Contains(stats.Body.String(), `"username":"alice"`) {
		t.Fatalf("statistics: %d %s", stats.Code, stats.Body)
	}
	if code := a.call("alice-token", http.MethodGet, scoringapi.AdminPrefix+"/batches/"+batch+"/export", nil).Code; code != http.StatusForbidden {
		t.Fatalf("alice exported: %d", code)
	}
	export := a.call("admin-token", http.MethodGet, scoringapi.AdminPrefix+"/batches/"+batch+"/export", nil)
	if export.Code != http.StatusOK || export.Header().Get("Content-Type") != "application/zip" ||
		!strings.Contains(export.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %v", export.Code, export.Header())
	}
	if code := a.call("admin-token", http.MethodDelete, scoringapi.AdminPrefix+"/batches/"+batch, nil).Code; code != http.StatusConflict {
		t.Fatalf("deleting a rated batch: %d", code)
	}
}
