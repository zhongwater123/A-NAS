package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
)

func readSystemHealth(t *testing.T, handler http.Handler) (string, []healthFindingResponse) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/host-state", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		System struct {
			Health         string                  `json:"health"`
			HealthFindings []healthFindingResponse `json:"healthFindings"`
		} `json:"system"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.System.Health, body.System.HealthFindings
}

// The system's health is assessed from what was observed, not taken from the
// reader, which on Linux never rates the system as a whole.
func TestHostStateAssessesTheSystemHealth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	warning := newStateHandler(fake.NewWarning(), DataSourceSimulated, "test-version", logger)
	health, findings := readSystemHealth(t, warning)
	temperature := int64(55)
	want := []healthFindingResponse{
		{Code: "disk_warning", Severity: "warning", DiskID: "disk:fake-data-01"},
		{Code: "disk_hot", Severity: "warning", DiskID: "disk:fake-data-01", Value: &temperature},
	}
	if health != "warning" || !reflect.DeepEqual(findings, want) {
		t.Fatalf("health = %q, findings = %+v; want warning, %+v", health, findings, want)
	}

	// A created data volume that no disk carries is offline.
	offline := newStateHandler(fake.NewHealthy(), DataSourceSimulated, "test-version", logger)
	offline.dataVolumeExpected = func() bool { return true }
	health, findings = readSystemHealth(t, offline)
	if health != "critical" || len(findings) != 1 || findings[0].Code != "volume_offline" || findings[0].DiskID != "" {
		t.Fatalf("health = %q, findings = %+v; want critical, volume_offline", health, findings)
	}
}
