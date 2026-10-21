package httpapi_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/voice"
	"github.com/zhongwater123/A-NAS/internal/voiceapi"
)

// voiceMedia decodes every file to 20 seconds of AAC speech.
type voiceMedia struct{}

func (voiceMedia) Probe(context.Context, *os.File) (mediaworker.Probe, error) {
	return mediaworker.Probe{Format: mediaworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 20},
		Streams: []mediaworker.ProbeStream{{Type: "audio", Codec: "aac"}}}, nil
}

func (voiceMedia) Audio(context.Context, *os.File, mediaworker.AudioRequest) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("fLaC")), nil
}

func (voiceMedia) Speech(_ context.Context, _ *os.File, request mediaworker.SpeechRequest) (io.ReadCloser, error) {
	if request.Format == mediaworker.SpeechOpus {
		return io.NopCloser(strings.NewReader("OggS")), nil
	}
	samples := make([]byte, 2*int(request.Duration*16000))
	for i := 0; i < len(samples); i += 2 {
		binary.LittleEndian.PutUint16(samples[i:], uint16(i%300))
	}
	return io.NopCloser(bytes.NewReader(samples)), nil
}

// voiceContract is 语音处理 acting for an administrator, with one processed
// recording, a second audio file not yet imported and the members' accounts.
type voiceContract struct {
	handler, member    http.Handler
	recording, another string
	accounts           *accounts.Service
	files              *files.Service
	service            *voice.Service
}

func voiceAPI(t *testing.T) voiceContract {
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
	member, err := accountService.CreateMember(ctx, admin, "member", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	var personal string
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindPrivate && space.OwnerUserID == admin.ID {
			personal = space.ID
		}
	}
	fileStore, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	fileService := files.NewService(fileStore, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	first, err := fileService.Upload(ctx, admin, personal, "", "周会.m4a", strings.NewReader("audio"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := fileService.Upload(ctx, admin, personal, "", "访谈.m4a", strings.NewReader("audio"))
	if err != nil {
		t.Fatal(err)
	}
	voiceStore, err := voice.OpenSQLite(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	service := voice.New(voiceStore, fileService, voice.Options{
		Media: voiceMedia{}, Recognizer: voice.FakeRecognizer{}, TextModel: voice.FakeTextModel{}, Voiceprints: voice.FakeVoiceprints{},
		Sessions: func(context.Context) []accounts.Session { return []accounts.Session{{Token: "contract", User: admin}} },
		Poll:     20 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	service.Run()
	t.Cleanup(func() {
		service.Close()
		_ = voiceStore.Close()
	})
	recording, err := service.Import(ctx, admin, voice.ImportRequest{FileID: first.ID, Template: "meeting", TranslateTo: "en"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		detail, err := service.Get(ctx, admin, recording.ID)
		if err == nil && detail.Status == voice.StatusReady && len(detail.Summaries) == 1 && detail.Summaries[0].Status == voice.JobReady &&
			len(detail.Translations) == 1 && detail.Translations[0].Status == voice.JobReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the recording was not processed: %+v, %v", detail.Recording, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	api := voiceapi.New(service, nil)
	as := func(user accounts.User) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			api.ServeHTTP(w, r.WithContext(voiceapi.WithUser(r.Context(), user)))
		})
	}
	return voiceContract{handler: as(admin), member: as(member), recording: recording.ID, another: second.ID,
		accounts: accountService, files: fileService, service: service}
}

func TestVoiceAPIUsesTheSessionUserAndRequiresCSRFForWrites(t *testing.T) {
	contract := voiceAPI(t)
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: contract.accounts, Files: contract.files,
		Voice: voiceapi.New(contract.service, nil), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/voice/recordings", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}
	login := func(username string) (*http.Cookie, string) {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"username":"`+username+`","password":"correct horse battery staple"}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("login %s = %d %s", username, recorder.Code, recorder.Body.String())
		}
		var session struct {
			CSRFToken string `json:"csrfToken"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		return recorder.Result().Cookies()[0], session.CSRFToken
	}
	send := func(cookie *http.Cookie, method, path, body, csrf string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.AddCookie(cookie)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	owner, ownerCSRF := login("owner")
	if got := send(owner, http.MethodGet, "/api/v1/voice/recordings/"+contract.recording, "", ""); got.Code != http.StatusOK {
		t.Fatalf("owner reads the recording: %d", got.Code)
	}
	if got := send(owner, http.MethodPatch, "/api/v1/voice/recordings/"+contract.recording, `{"title":"x"}`, ""); got.Code != http.StatusForbidden {
		t.Fatalf("a write without CSRF = %d", got.Code)
	}
	if got := send(owner, http.MethodPatch, "/api/v1/voice/recordings/"+contract.recording, `{"title":"周会纪要"}`, ownerCSRF); got.Code != http.StatusOK {
		t.Fatalf("a write with CSRF = %d %s", got.Code, got.Body.String())
	}
	member, memberCSRF := login("member")
	if got := send(member, http.MethodGet, "/api/v1/voice/recordings/"+contract.recording, "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("another member reads the recording: %d", got.Code)
	}
	if got := send(member, http.MethodGet, "/api/v1/voice/settings/recognizer", "", ""); got.Code != http.StatusForbidden {
		t.Fatalf("a member reads the recognizer settings: %d", got.Code)
	}
	if got := send(member, http.MethodPost, "/api/v1/voice/recordings", `{"fileId":"`+contract.another+`"}`, memberCSRF); got.Code != http.StatusNotFound {
		t.Fatalf("a member imports the owner's file: %d %s", got.Code, got.Body.String())
	}
}
