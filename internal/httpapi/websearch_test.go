package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/websearch"
	"github.com/zhongwater123/A-NAS/internal/websearchapi"
)

type webSearchStatus struct {
	Enabled    bool     `json:"enabled"`
	Running    string   `json:"running"`
	Torrents   bool     `json:"torrents"`
	Categories []string `json:"categories"`
}

type webSearchPage struct {
	Results []struct {
		URL       string `json:"url"`
		Thumbnail string `json:"thumbnail"`
		Magnet    string `json:"magnet"`
		SizeBytes int64  `json:"sizeBytes"`
		Seeders   int    `json:"seeders"`
	} `json:"results"`
	Unresponsive []string `json:"unresponsive"`
}

func TestWebSearchFollowsTheContractAndItsSwitches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := accounts.OpenSQLite(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	settings, err := websearch.OpenStore(filepath.Join(dir, "web-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := websearch.New(websearch.Options{
		Backend: websearch.Fake{}, Switch: websearch.NewMemorySwitch(true), Settings: settings,
		Secret: []byte("test secret"), ThumbnailPath: websearchapi.ThumbnailPath,
	})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService,
		WebSearch: websearchapi.New(service, websearch.NewRelease(filepath.Join(dir, "no-release")), logger), Logger: logger,
	})
	f := &activityFixture{t: t, handler: handler, accounts: accountService, client: &contractClient{t: t, handler: handler}}
	asAdmin := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
	session, err := accountService.ResolveSession(ctx, asAdmin.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountService.CreateMember(ctx, session.User, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	asAlice := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)

	status := func(client *contractClient) webSearchStatus {
		t.Helper()
		var status webSearchStatus
		if err := json.Unmarshal(client.do(http.MethodGet, "/api/v1/web-search", "", nil, http.StatusOK).Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	search := func(client *contractClient, body string, want int) webSearchPage {
		t.Helper()
		var page webSearchPage
		response := client.do(http.MethodPost, "/api/v1/web-search/query", body, nil, want)
		if want == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
		}
		return page
	}

	if got := status(asAlice); !got.Enabled || strings.Join(got.Categories, ",") != "web,images,videos,news" {
		t.Fatalf("status = %+v, want web search on without the torrent tab", got)
	}
	page := search(asAlice, `{"q":"流浪地球","category":"videos"}`, http.StatusOK)
	if len(page.Results) == 0 || !strings.HasPrefix(page.Results[0].Thumbnail, websearchapi.ThumbnailPath+"?") || len(page.Unresponsive) != 1 {
		t.Fatalf("videos = %+v", page)
	}
	thumbnail := asAlice.do(http.MethodGet, page.Results[0].Thumbnail, "", nil, http.StatusOK)
	if thumbnail.Header().Get("Content-Type") != "image/png" || thumbnail.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("thumbnail headers = %v", thumbnail.Header())
	}
	asAlice.do(http.MethodGet, page.Results[0].Thumbnail+"0", "", nil, http.StatusBadRequest)
	search(asAlice, `{"q":"ubuntu","category":"torrents"}`, http.StatusForbidden)

	asAlice.do(http.MethodPut, "/api/v1/web-search", `{"enabled":true,"torrents":true}`, nil, http.StatusForbidden)
	asAdmin.do(http.MethodPut, "/api/v1/web-search", `{"enabled":true,"torrents":true}`, nil, http.StatusOK)
	if got := status(asAlice); !got.Torrents || got.Categories[len(got.Categories)-1] != "torrents" {
		t.Fatalf("status = %+v, want the torrent tab", got)
	}
	torrents := search(asAlice, `{"q":"ubuntu","category":"torrents","page":2}`, http.StatusOK)
	if len(torrents.Results) == 0 || !strings.HasPrefix(torrents.Results[0].Magnet, "magnet:?") || torrents.Results[0].SizeBytes == 0 {
		t.Fatalf("torrents = %+v", torrents)
	}

	asAdmin.do(http.MethodPut, "/api/v1/web-search", `{"enabled":false,"torrents":true}`, nil, http.StatusOK)
	if got := status(asAlice); got.Enabled || got.Running != "stopped" || len(got.Categories) != 0 {
		t.Fatalf("status = %+v, want web search off", got)
	}
	search(asAlice, `{"q":"天气","category":"web"}`, http.StatusForbidden)
	asAlice.do(http.MethodGet, page.Results[0].Thumbnail, "", nil, http.StatusForbidden)
	asAdmin.do(http.MethodPut, "/api/v1/web-search", `{"enabled":true,"torrents":false}`, nil, http.StatusOK)

	// Each account searches ten times in a row, then once every two seconds;
	// refused searches above took no turn.
	for range 10 {
		search(asAdmin, `{"q":"天气","category":"news","timeRange":"week"}`, http.StatusOK)
	}
	limited := asAdmin.do(http.MethodPost, "/api/v1/web-search/query", `{"q":"天气","category":"news"}`, nil, http.StatusTooManyRequests)
	if limited.Header().Get("Retry-After") != "2" {
		t.Fatalf("Retry-After = %q", limited.Header().Get("Retry-After"))
	}
	search(asAlice, `{"q":"天气","category":"web"}`, http.StatusOK)

	asAlice.do(http.MethodGet, "/api/v1/web-search/license", "", nil, http.StatusNotFound)
	asAlice.do(http.MethodGet, "/api/v1/web-search/source", "", nil, http.StatusNotFound)
}
