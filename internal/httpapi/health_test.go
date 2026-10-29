package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
)

type systemStorageReader struct{ *fake.Reader }

func (r systemStorageReader) Read(ctx context.Context) (hoststate.State, error) {
	state, err := r.Reader.Read(ctx)
	state.System.Storage = &hoststate.StorageUsage{TotalBytes: 100 << 30, AvailableBytes: 25 << 30}
	return state, err
}

func TestSystemStorageReachesBothStateEndpoints(t *testing.T) {
	handler := newStateHandler(systemStorageReader{fake.NewHealthy()}, DataSourceSimulated, "test-version", slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range []string{"/api/v1/system", "/api/v1/host-state"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %s", path, recorder.Body.String())
		}
		var response struct {
			Storage *systemStorageResponse `json:"storage"`
			System  struct {
				Storage *systemStorageResponse `json:"storage"`
			} `json:"system"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		usage := response.Storage
		if path == "/api/v1/host-state" {
			usage = response.System.Storage
		}
		if usage == nil || usage.TotalBytes != 100<<30 || usage.AvailableBytes != 25<<30 {
			t.Fatalf("%s usage = %+v", path, usage)
		}
	}
}

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
