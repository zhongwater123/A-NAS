package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

func TestProductAPIRequiresLoginAndExposesOnlyVisibleSpaces(t *testing.T) {
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	fileStore, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	fileService := files.NewService(fileStore, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	storageService := storage.NewService(fake.NewHealthy(), apiVolumeExecutor{}, storage.Options{})
	terminalCalls := 0
	terminalHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		terminalCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated,
		ProductVersion: "v1.0.1-rc.1", Accounts: accountService, Files: fileService, Storage: storageService,
		Terminal: terminalHandler, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/disks", nil))
	if got, want := unauthorized.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("unauthenticated disks status = %d, want %d", got, want)
	}
	unauthorizedMetrics := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedMetrics, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))
	if got, want := unauthorizedMetrics.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("unauthenticated metrics status = %d, want %d", got, want)
	}
	unauthorizedTerminal := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedTerminal, httptest.NewRequest(http.MethodGet, "/api/v1/terminal", nil))
	if got, want := unauthorizedTerminal.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("unauthenticated terminal status = %d, want %d", got, want)
	}

	setupBody := bytes.NewBufferString(`{"username":"owner","password":"correct horse battery staple"}`)
	setupRequest := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", setupBody)
	setupRequest.Header.Set("Content-Type", "application/json")
	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, setupRequest)
	if got, want := setup.Code, http.StatusCreated; got != want {
		t.Fatalf("setup status = %d, want %d; body=%s", got, want, setup.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &session); err != nil || session.CSRFToken == "" {
		t.Fatalf("setup session = %#v, err=%v", session, err)
	}
	responseCookies := setup.Result().Cookies()
	if len(responseCookies) != 1 || responseCookies[0].Name != "anas_session" || !responseCookies[0].HttpOnly {
		t.Fatalf("setup cookies = %#v", responseCookies)
	}

	disksRequest := httptest.NewRequest(http.MethodGet, "/api/v1/disks", nil)
	disksRequest.AddCookie(responseCookies[0])
	disks := httptest.NewRecorder()
	handler.ServeHTTP(disks, disksRequest)
	if got, want := disks.Code, http.StatusOK; got != want {
		t.Fatalf("authenticated disks status = %d, want %d; body=%s", got, want, disks.Body.String())
	}
	metricsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	metricsRequest.AddCookie(responseCookies[0])
	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, metricsRequest)
	if got, want := metrics.Code, http.StatusOK; got != want {
		t.Fatalf("authenticated metrics status = %d, want %d; body=%s", got, want, metrics.Body.String())
	}
	adminTerminalRequest := httptest.NewRequest(http.MethodGet, "/api/v1/terminal", nil)
	adminTerminalRequest.AddCookie(responseCookies[0])
	adminTerminal := httptest.NewRecorder()
	handler.ServeHTTP(adminTerminal, adminTerminalRequest)
	if got, want := adminTerminal.Code, http.StatusNoContent; got != want || terminalCalls != 1 {
		t.Fatalf("administrator terminal status/calls = %d/%d, want %d/1", got, terminalCalls, want)
	}

	spacesRequest := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
	spacesRequest.AddCookie(responseCookies[0])
	spaces := httptest.NewRecorder()
	handler.ServeHTTP(spaces, spacesRequest)
	if got, want := spaces.Code, http.StatusOK; got != want {
		t.Fatalf("spaces status = %d, want %d; body=%s", got, want, spaces.Body.String())
	}
	var spaceResponse struct {
		Items []accounts.Space `json:"items"`
	}
	if err := json.Unmarshal(spaces.Body.Bytes(), &spaceResponse); err != nil {
		t.Fatalf("decode spaces: %v", err)
	}
	if got, want := len(spaceResponse.Items), 2; got != want {
		t.Fatalf("visible spaces = %d, want %d", got, want)
	}
	var privateSpace accounts.Space
	for _, space := range spaceResponse.Items {
		if space.Kind == accounts.SpaceKindPrivate {
			privateSpace = space
		}
	}
	directoryRequest := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/"+privateSpace.ID+"/directories", bytes.NewBufferString(`{"parentId":"","name":"Docs"}`))
	directoryRequest.Header.Set("Content-Type", "application/json")
	directoryRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	directoryRequest.AddCookie(responseCookies[0])
	directoryRecorder := httptest.NewRecorder()
	handler.ServeHTTP(directoryRecorder, directoryRequest)
	if got, want := directoryRecorder.Code, http.StatusCreated; got != want {
		t.Fatalf("create directory status = %d, want %d; body=%s", got, want, directoryRecorder.Body.String())
	}
	var directory files.Entry
	if err := json.Unmarshal(directoryRecorder.Body.Bytes(), &directory); err != nil {
		t.Fatalf("decode directory: %v", err)
	}

	var uploadBody bytes.Buffer
	uploadWriter := multipart.NewWriter(&uploadBody)
	_ = uploadWriter.WriteField("parentId", directory.ID)
	part, err := uploadWriter.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	_, _ = part.Write([]byte("hello through API"))
	_ = uploadWriter.Close()
	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/"+privateSpace.ID+"/uploads", &uploadBody)
	uploadRequest.Header.Set("Content-Type", uploadWriter.FormDataContentType())
	uploadRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	uploadRequest.AddCookie(responseCookies[0])
	uploadRecorder := httptest.NewRecorder()
	handler.ServeHTTP(uploadRecorder, uploadRequest)
	if got, want := uploadRecorder.Code, http.StatusCreated; got != want {
		t.Fatalf("upload status = %d, want %d; body=%s", got, want, uploadRecorder.Body.String())
	}
	var uploaded files.Entry
	if err := json.Unmarshal(uploadRecorder.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("decode upload: %v", err)
	}
	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+uploaded.ID+"/content", nil)
	downloadRequest.AddCookie(responseCookies[0])
	download := httptest.NewRecorder()
	handler.ServeHTTP(download, downloadRequest)
	if got, want := download.Code, http.StatusOK; got != want || download.Body.String() != "hello through API" {
		t.Fatalf("download status/body = %d/%q, want 200/content", got, download.Body.String())
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/files/"+uploaded.ID, nil)
	deleteRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	deleteRequest.AddCookie(responseCookies[0])
	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, deleteRequest)
	if got, want := deleted.Code, http.StatusOK; got != want {
		t.Fatalf("delete status = %d, want %d; body=%s", got, want, deleted.Body.String())
	}
	var trash files.TrashItem
	if err := json.Unmarshal(deleted.Body.Bytes(), &trash); err != nil {
		t.Fatalf("decode trash item: %v", err)
	}
	restoreRequest := httptest.NewRequest(http.MethodPost, "/api/v1/trash/"+trash.ID+"/restore", bytes.NewBufferString(`{"parentId":"`+directory.ID+`","name":"restored.txt"}`))
	restoreRequest.Header.Set("Content-Type", "application/json")
	restoreRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	restoreRequest.AddCookie(responseCookies[0])
	restored := httptest.NewRecorder()
	handler.ServeHTTP(restored, restoreRequest)
	if got, want := restored.Code, http.StatusOK; got != want {
		t.Fatalf("restore status = %d, want %d; body=%s", got, want, restored.Body.String())
	}
	snapshotRequest := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/"+privateSpace.ID+"/snapshots", bytes.NewBufferString(`{"name":"manual-1"}`))
	snapshotRequest.Header.Set("Content-Type", "application/json")
	snapshotRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	snapshotRequest.AddCookie(responseCookies[0])
	snapshot := httptest.NewRecorder()
	handler.ServeHTTP(snapshot, snapshotRequest)
	if got, want := snapshot.Code, http.StatusCreated; got != want {
		t.Fatalf("snapshot status = %d, want %d; body=%s", got, want, snapshot.Body.String())
	}

	memberBody := bytes.NewBufferString(`{"username":"alice","password":"alice password for testing"}`)
	memberRequest := httptest.NewRequest(http.MethodPost, "/api/v1/users", memberBody)
	memberRequest.Header.Set("Content-Type", "application/json")
	memberRequest.AddCookie(responseCookies[0])
	missingCSRF := httptest.NewRecorder()
	handler.ServeHTTP(missingCSRF, memberRequest)
	if got, want := missingCSRF.Code, http.StatusForbidden; got != want {
		t.Fatalf("missing CSRF status = %d, want %d", got, want)
	}

	memberRequest = httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewBufferString(`{"username":"alice","password":"alice password for testing"}`))
	memberRequest.Header.Set("Content-Type", "application/json")
	memberRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	memberRequest.AddCookie(responseCookies[0])
	member := httptest.NewRecorder()
	handler.ServeHTTP(member, memberRequest)
	if got, want := member.Code, http.StatusCreated; got != want {
		t.Fatalf("create member status = %d, want %d; body=%s", got, want, member.Body.String())
	}
	var createdMember accounts.User
	if err := json.Unmarshal(member.Body.Bytes(), &createdMember); err != nil {
		t.Fatal(err)
	}
	memberSession, err := accountService.Authenticate(context.Background(), "alice", "alice password for testing")
	if err != nil {
		t.Fatalf("authenticate member: %v", err)
	}
	memberTerminalRequest := httptest.NewRequest(http.MethodGet, "/api/v1/terminal", nil)
	memberTerminalRequest.AddCookie(&http.Cookie{Name: "anas_session", Value: memberSession.Token})
	memberTerminal := httptest.NewRecorder()
	handler.ServeHTTP(memberTerminal, memberTerminalRequest)
	if got, want := memberTerminal.Code, http.StatusForbidden; got != want || terminalCalls != 1 {
		t.Fatalf("member terminal status/calls = %d/%d, want %d/1", got, terminalCalls, want)
	}
	resetRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+createdMember.ID+"/credential", bytes.NewBufferString(`{"password":"alice reset password"}`))
	resetRequest.Header.Set("Content-Type", "application/json")
	resetRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	resetRequest.AddCookie(responseCookies[0])
	reset := httptest.NewRecorder()
	handler.ServeHTTP(reset, resetRequest)
	if reset.Code != http.StatusNoContent {
		t.Fatalf("reset credential status = %d; body=%s", reset.Code, reset.Body.String())
	}
	disableRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+createdMember.ID, nil)
	disableRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	disableRequest.AddCookie(responseCookies[0])
	disable := httptest.NewRecorder()
	handler.ServeHTTP(disable, disableRequest)
	if disable.Code != http.StatusNoContent {
		t.Fatalf("disable member status = %d; body=%s", disable.Code, disable.Body.String())
	}

	planRequest := httptest.NewRequest(http.MethodPost, "/api/v1/storage/plans", bytes.NewBufferString(`{"diskId":"disk:fake-data-01"}`))
	planRequest.Header.Set("Content-Type", "application/json")
	planRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	planRequest.AddCookie(responseCookies[0])
	planRecorder := httptest.NewRecorder()
	handler.ServeHTTP(planRecorder, planRequest)
	if got, want := planRecorder.Code, http.StatusCreated; got != want {
		t.Fatalf("storage plan status = %d, want %d; body=%s", got, want, planRecorder.Body.String())
	}
	var plan storage.ExecutionPlan
	if err := json.Unmarshal(planRecorder.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode storage plan: %v", err)
	}
	confirmRequest := httptest.NewRequest(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/confirm", bytes.NewBufferString(`{"confirmationPhrase":"`+plan.ConfirmationPhrase+`"}`))
	confirmRequest.Header.Set("Content-Type", "application/json")
	confirmRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	confirmRequest.AddCookie(responseCookies[0])
	confirm := httptest.NewRecorder()
	handler.ServeHTTP(confirm, confirmRequest)
	if got, want := confirm.Code, http.StatusOK; got != want {
		t.Fatalf("confirm plan status = %d, want %d; body=%s", got, want, confirm.Body.String())
	}
	executeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/execute", bytes.NewBufferString(`{}`))
	executeRequest.Header.Set("Content-Type", "application/json")
	executeRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	executeRequest.AddCookie(responseCookies[0])
	execute := httptest.NewRecorder()
	handler.ServeHTTP(execute, executeRequest)
	if got, want := execute.Code, http.StatusOK; got != want {
		t.Fatalf("execute plan status = %d, want %d; body=%s", got, want, execute.Body.String())
	}
	volumesRequest := httptest.NewRequest(http.MethodGet, "/api/v1/volumes", nil)
	volumesRequest.AddCookie(responseCookies[0])
	volumes := httptest.NewRecorder()
	handler.ServeHTTP(volumes, volumesRequest)
	if got, want := volumes.Code, http.StatusOK; got != want || !bytes.Contains(volumes.Body.Bytes(), []byte(`"id":"volume:data"`)) {
		t.Fatalf("volumes status/body = %d/%s, want persisted volume", got, volumes.Body.String())
	}
	auditRequest := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
	auditRequest.AddCookie(responseCookies[0])
	audit := httptest.NewRecorder()
	handler.ServeHTTP(audit, auditRequest)
	if got, want := audit.Code, http.StatusOK; got != want || !bytes.Contains(audit.Body.Bytes(), []byte("file.uploaded")) {
		t.Fatalf("audit status/body = %d/%s, want operation evidence", got, audit.Body.String())
	}
}

func TestProductAPIReportsCredentialProvisioningFailure(t *testing.T) {
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated,
		Accounts: accounts.NewService(store, failingCredentials{}, accounts.Options{}),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", bytes.NewBufferString(
		`{"username":"owner","password":"correct horse battery staple"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got, want := response.Code, http.StatusServiceUnavailable; got != want || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"credential_provision_failed"`)) {
		t.Fatalf("setup status/body = %d/%s, want typed credential failure", got, response.Body.String())
	}
}

type apiCredentials struct{}

func (apiCredentials) SetCredential(context.Context, accounts.CredentialRequest) error { return nil }
func (apiCredentials) DisableCredential(context.Context, string) error                 { return nil }
func (apiCredentials) GrantViewing(context.Context, accounts.ViewingRequest) error     { return nil }
func (apiCredentials) RevokeViewing(context.Context, string) error                     { return nil }

type failingCredentials struct{}

func (failingCredentials) SetCredential(context.Context, accounts.CredentialRequest) error {
	return errors.New("host account sandbox rejected useradd")
}
func (failingCredentials) DisableCredential(context.Context, string) error { return nil }

type apiVolumeExecutor struct{}

func (apiVolumeExecutor) CreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	return storage.Volume{ID: "volume:data", DiskID: request.DiskID, State: storage.VolumeStateAvailable}, nil
}
