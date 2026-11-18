package httpapi_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
)

// shortVideos reads every file as a short video.
type shortVideos struct{}

func (shortVideos) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{
		Format:  ffmpegworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 8},
		Streams: []ffmpegworker.ProbeStream{{Type: "video", Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080}},
	}, nil
}
func (shortVideos) Image(context.Context, *os.File, int) ([]byte, error) {
	return nil, ffmpegworker.ErrUnreadable
}
func (shortVideos) Frame(context.Context, *os.File, float64, int) ([]byte, error) {
	return nil, ffmpegworker.ErrUnreadable
}
func (shortVideos) Stream(context.Context, *os.File, ffmpegworker.StreamRequest) (io.ReadCloser, error) {
	return nil, ffmpegworker.ErrUnreadable
}

func shortVideo(seed byte) []byte {
	b := make([]byte, 600)
	binary.BigEndian.PutUint32(b, 24)
	copy(b[4:], "ftypisom")
	b[len(b)-1] = seed
	return b
}

// TestMontageCenterKeepsToTheContract drives 剪辑中心 through the Product
// Service as development runs it, with the photo library's grants in the
// same process, and with its switch off.
func TestMontageCenterKeepsToTheContract(t *testing.T) {
	dir := t.TempDir()
	controlDB := filepath.Join(dir, "control.db")
	store, err := accounts.OpenSQLite(controlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	users := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	photoService, err := photos.Open(filepath.Join(dir, "photos"), photos.Options{DisableCapacityReserve: true, Media: shortVideos{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = photoService.Close() })
	sessions := sessionDirectory(t, controlDB)
	if err := os.Mkdir(filepath.Join(dir, "montage"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Jobs are queued and polled here; no job runs.
	montageStore, err := montage.Open(t.Context(), filepath.Join(dir, "montage"),
		montage.Options{Grants: photoservice.LocalGrants(photoService, sessions, logger), Logger: logger,
			JobCompute: idleCompute{}, Editing: ffmpegworker.Local{}, Planner: montage.FixedPlanner{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = montageStore.Close() })
	// The header that says a planning grant was registered reaches the
	// montage center only when the Product Service sets it.
	var forwarded []string
	api := montageapi.New(montageStore, logger)
	recorded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = append(forwarded, r.Header.Get(montageapi.AssistantHeader))
		api.ServeHTTP(w, r)
	})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users,
		Montage: recorded, Logger: logger,
	})
	f := &activityFixture{t: t, handler: handler, client: &contractClient{t: t, handler: handler}}
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	owner.do(http.MethodPost, "/api/v1/users", `{"username":"bob","password":"bob password for testing"}`, nil, http.StatusCreated)
	bob := f.signIn("/api/v1/session", "bob", "bob password for testing", "172.18.45.21", http.StatusOK)
	response := owner.do(http.MethodGet, "/api/v1/montage/projects", "", map[string]string{montageapi.AssistantHeader: "on"}, http.StatusOK)
	if body := strings.TrimSpace(response.Body.String()); body != `{"items":[]}` {
		t.Fatalf("projects = %s", body)
	}
	if len(forwarded) != 1 || forwarded[0] != "" {
		t.Fatalf("the montage center saw %s = %q", montageapi.AssistantHeader, forwarded)
	}
	anonymous := &contractClient{t: t, handler: handler}
	anonymous.do(http.MethodGet, "/api/v1/montage/projects", "", nil, http.StatusUnauthorized)

	// The owner's two videos, in the private library.
	var me struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	decode(t, owner.do(http.MethodGet, "/api/v1/session", "", nil, http.StatusOK).Body, &me)
	principal := photos.Principal{UserID: me.User.ID, Username: me.User.Username}
	libraries, err := photoService.Libraries(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	var videos []string
	for _, lib := range libraries {
		if lib.OwnerUserID != principal.UserID {
			continue
		}
		for seed := range byte(2) {
			asset, err := photoService.Import(t.Context(), principal, photos.ImportRequest{LibraryID: lib.ID, Name: "clip.mp4", Content: bytes.NewReader(shortVideo(seed))})
			if err != nil {
				t.Fatal(err)
			}
			videos = append(videos, asset.ID)
		}
	}

	var project montage.ProjectDetail
	decode(t, owner.do(http.MethodPost, "/api/v1/montage/projects",
		`{"requestKey":"k1","name":"海边","assetIds":["`+videos[0]+`"]}`, nil, http.StatusCreated).Body, &project)
	owner.do(http.MethodPost, "/api/v1/montage/projects", `{"requestKey":"k1","name":"海边","assetIds":["`+videos[0]+`"]}`, nil, http.StatusOK)
	bob.do(http.MethodPost, "/api/v1/montage/projects", `{"requestKey":"k1","assetIds":["`+videos[0]+`"]}`, nil, http.StatusUnprocessableEntity)
	path := "/api/v1/montage/projects/" + project.ID
	bob.do(http.MethodGet, path, "", nil, http.StatusNotFound)
	owner.do(http.MethodPost, path+"/assets", `{"requestKey":"k3","assetIds":["`+videos[1]+`"]}`, nil, http.StatusOK)
	owner.do(http.MethodPatch, path, `{"name":"海边一日","chronological":false}`, nil, http.StatusOK)
	decode(t, owner.do(http.MethodGet, path, "", nil, http.StatusOK).Body, &project)
	if project.Name != "海边一日" || project.Chronological || project.AssetCount != 2 || project.Assets[1].Status != montage.AssetAvailable {
		t.Fatalf("project = %+v", project)
	}
	var page montage.ProjectPage
	decode(t, owner.do(http.MethodGet, "/api/v1/montage/projects", "", nil, http.StatusOK).Body, &page)
	if len(page.Items) != 1 || page.Items[0].AssetCount != 2 {
		t.Fatalf("projects = %+v", page)
	}

	// A film: 202 and the job to poll; one job at a time.
	var job montage.Job
	decode(t, owner.do(http.MethodPost, path+"/generate", `{"requestKey":"g1"}`, nil, http.StatusAccepted).Body, &job)
	if job.State != montage.JobQueued || len(job.Steps) != len(montage.StepNames) {
		t.Fatalf("job = %+v", job)
	}
	owner.do(http.MethodPost, path+"/generate", `{"requestKey":"g1"}`, nil, http.StatusAccepted)
	owner.do(http.MethodPost, path+"/generate", `{"requestKey":"g2"}`, nil, http.StatusConflict)
	bob.do(http.MethodPost, path+"/generate", `{"requestKey":"g3"}`, nil, http.StatusNotFound)
	jobPath := "/api/v1/montage/jobs/" + job.ID
	bob.do(http.MethodGet, jobPath, "", nil, http.StatusNotFound)
	owner.do(http.MethodGet, jobPath, "", nil, http.StatusOK)
	owner.do(http.MethodPost, jobPath+"/retry", "", nil, http.StatusConflict)
	owner.do(http.MethodPost, jobPath+"/drop-assets", `{"assetIds":["`+videos[1]+`"]}`, nil, http.StatusConflict)
	var notices struct {
		Items []montage.Notice `json:"items"`
	}
	decode(t, owner.do(http.MethodGet, "/api/v1/montage/notices", "", nil, http.StatusOK).Body, &notices)
	if len(notices.Items) != 0 {
		t.Fatalf("notices of a queued job = %+v", notices)
	}
	bob.do(http.MethodPost, jobPath+"/cancel", "", nil, http.StatusNotFound)
	decode(t, owner.do(http.MethodPost, jobPath+"/cancel", "", nil, http.StatusOK).Body, &job)
	if job.State != montage.JobCancelled {
		t.Fatalf("cancelled job = %+v", job)
	}
	owner.do(http.MethodPost, jobPath+"/cancel", "", nil, http.StatusConflict)

	bob.do(http.MethodDelete, path, "", nil, http.StatusNotFound)
	owner.do(http.MethodDelete, path, "", nil, http.StatusNoContent)
	owner.do(http.MethodGet, jobPath, "", nil, http.StatusNotFound)
	owner.do(http.MethodGet, path, "", nil, http.StatusNotFound)
	// The videos stay in the photo library.
	if found, err := photoService.Lookup(t.Context(), principal, videos); err != nil || len(found) != 2 {
		t.Fatalf("the videos after deleting the project = %+v, %v", found, err)
	}
	if grants, err := photoService.Grants(t.Context()); err != nil || len(grants) != 1 || !grants[0].Revoked {
		t.Fatalf("grants after deleting the project = %+v, %v", grants, err)
	}

	// Switched off, the API is not there at all.
	off := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users, Logger: logger,
	})
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/montage/projects", nil)
	request.AddCookie(owner.cookie)
	request.Header.Set("X-Forwarded-For", owner.forwardedFor)
	recorder := httptest.NewRecorder()
	off.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("switched off: %d %s", recorder.Code, recorder.Body)
	}
}

// idleCompute lets jobs be queued; no job runs in this test.
type idleCompute struct{}

func (idleCompute) Catalog(context.Context, montage.CatalogRequest) ([]string, error) {
	return nil, errors.New("no job runs here")
}

func (idleCompute) Fill(context.Context, montage.FillRequest, []float32, int) (montage.FillAnswer, error) {
	return montage.FillAnswer{}, errors.New("no job runs here")
}

func (idleCompute) PlanBrief(context.Context, montage.PlanBriefRequest) (montage.PlanBrief, error) {
	return montage.PlanBrief{}, errors.New("no job runs here")
}

func (idleCompute) ShotsAnswer(context.Context, json.RawMessage, json.RawMessage, []int) (json.RawMessage, error) {
	return nil, errors.New("no job runs here")
}

func (idleCompute) Clear(context.Context, montage.ClearRequest, []byte, []byte, []byte, int, int) ([2]float64, error) {
	return [2]float64{}, errors.New("no job runs here")
}
