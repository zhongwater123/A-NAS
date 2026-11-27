package httpapi_test

import (
	"bytes"
	"context"
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
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocut/external"
	"github.com/zhongwater123/A-NAS/internal/videocut/protocol"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// songMedia stands in for the media worker as VideoCut uses it:
// every file is two minutes of audio.
type songMedia struct{}

func (songMedia) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Name: "mp3", Duration: 120},
		Streams: []ffmpegworker.ProbeStream{{Type: "audio", Codec: "mp3"}}}, nil
}

func (songMedia) Audio(_ context.Context, _ *os.File, request ffmpegworker.AudioRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(make([]byte, 4*request.Channels*16))), nil
}

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

// TestVideoCutCenterKeepsToTheContract drives 剪辑中心 through the Product
// Service as development runs it, with the photo library's grants in the
// same process, and with its switch off.
func TestVideoCutCenterKeepsToTheContract(t *testing.T) {
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
	if err := os.Mkdir(filepath.Join(dir, "videocut"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Jobs are queued and polled here; no job runs.
	videoCutStore, err := app.Open(t.Context(), filepath.Join(dir, "videocut"),
		app.Options{Grants: photoservice.LocalGrants(photoService, sessions, nil, logger), Logger: logger,
			Media: songMedia{}, JobCompute: idleCompute{}, Editing: ffmpegworker.Local{}, Planner: external.FixedPlanner{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = videoCutStore.Close() })
	// Whether the switch is on is the Product Service's answer alone.
	served := 0
	api := protocol.New(videoCutStore, logger)
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		api.ServeHTTP(w, r)
	})
	// The File Broker's part: a planning grant for the member behind the request.
	grants := &planGrants{plan: wire.PlanRegistered}
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users,
		// The headers VideoCut gets are those its socket would carry.
		VideoCut: serviceproxy.Local(counted), PlanGrants: grants, Logger: logger,
	})
	f := &activityFixture{t: t, handler: handler, client: &contractClient{t: t, handler: handler}}
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	owner.do(http.MethodPost, "/api/v1/users", `{"username":"bob","password":"bob password for testing"}`, nil, http.StatusCreated)
	bob := f.signIn("/api/v1/session", "bob", "bob password for testing", "172.18.45.21", http.StatusOK)
	owner.do(http.MethodGet, "/api/v1/videocut", "", nil, http.StatusNoContent)
	if served != 0 {
		t.Fatal("VideoCut was asked whether it is on")
	}
	anonymous := &contractClient{t: t, handler: handler}
	anonymous.do(http.MethodGet, "/api/v1/videocut", "", nil, http.StatusUnauthorized)
	anonymous.do(http.MethodGet, "/api/v1/videocut/projects", "", nil, http.StatusUnauthorized)

	// Venus's operations answer 501 until VideoCut serves them (#694); the
	// Web answers them from its MSW stand-ins meanwhile. The requests that may
	// lead to planning still register a grant behind the member's session
	// first: while the broker cannot register one they answer
	// assistant_unavailable, and VideoCut is not asked.
	grants.err = errors.New("the File Broker is restarting")
	before := served
	for _, route := range []string{"/api/v1/videocut/projects/project:x/generate", "/api/v1/videocut/projects/project:x/retry"} {
		response := owner.do(http.MethodPost, route, `{"requestKey":"g1"}`, nil, http.StatusServiceUnavailable)
		if !strings.Contains(response.Body.String(), `"assistant_unavailable"`) {
			t.Fatalf("%s without a planning grant: %s", route, response.Body.String())
		}
	}
	if served != before || grants.registered != 0 {
		t.Fatal("a request whose planning grant was not registered reached VideoCut")
	}
	var notices struct {
		Items []domain.Notice `json:"items"`
	}
	decode(t, bob.do(http.MethodGet, "/api/v1/videocut/notices", "", nil, http.StatusOK).Body, &notices)
	if len(notices.Items) != 0 {
		t.Fatalf("notices = %+v", notices)
	}

	// Switched off, the API is not there at all.
	off := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users, Logger: logger,
	})
	for _, path := range []string{"/api/v1/videocut", "/api/v1/videocut/projects"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+path, nil)
		request.AddCookie(owner.cookie)
		request.Header.Set("X-Forwarded-For", owner.forwardedFor)
		recorder := httptest.NewRecorder()
		off.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("switched off, %s: %d %s", path, recorder.Code, recorder.Body)
		}
	}
}

// planGrants stands in for the File Broker's assistant_plan.
type planGrants struct {
	plan       string
	err        error
	registered int
}

func (g *planGrants) RegisterAssistantPlan(ctx context.Context) (string, error) {
	if accounts.SessionToken(ctx) == "" {
		return "", errors.New("no session for the planning grant")
	}
	if g.err != nil {
		return "", g.err
	}
	g.registered++
	return g.plan, nil
}

// idleCompute lets jobs be queued; no job runs in this test.
type idleCompute struct{}

func (idleCompute) Catalog(context.Context, videocutcompute.CatalogRequest) ([]string, error) {
	return nil, errors.New("no job runs here")
}

func (idleCompute) Fill(context.Context, videocutcompute.FillRequest, []float32, int) (videocutcompute.FillAnswer, error) {
	return videocutcompute.FillAnswer{}, errors.New("no job runs here")
}

func (idleCompute) Likeness(context.Context, []float32, []float32, int) (float64, error) {
	return 0, errors.New("no job runs here")
}

func (idleCompute) PlanBrief(context.Context, videocutcompute.PlanBriefRequest) (videocutcompute.PlanBrief, error) {
	return videocutcompute.PlanBrief{}, errors.New("no job runs here")
}

func (idleCompute) ShotsAnswer(context.Context, json.RawMessage, json.RawMessage, []int) (json.RawMessage, error) {
	return nil, errors.New("no job runs here")
}

func (idleCompute) Clear(context.Context, videocutcompute.ClearRequest, []byte, []byte, []byte, int, int) ([2]float64, error) {
	return [2]float64{}, errors.New("no job runs here")
}
