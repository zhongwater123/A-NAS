package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

func TestHealthEndpointReportsLiveProcess(t *testing.T) {
	handler := newHandler(fake.NewHealthy())
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := recorder.Body.String(), "{\"status\":\"ok\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestSystemEndpointReturnsObservedHostState(t *testing.T) {
	handler := newHandler(fake.NewHealthy())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d; body = %s", got, want, recorder.Body.String())
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	const want = "{\"id\":\"host:fake-01\",\"hostname\":\"anas-fake\",\"operatingSystem\":{\"name\":\"Debian\",\"version\":\"13\"},\"architecture\":\"amd64\",\"uptimeSeconds\":3600,\"health\":\"healthy\",\"productVersion\":\"test-version\",\"observedAt\":\"2026-10-06T00:00:00Z\"}\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestDisksEndpointReturnsStableOrderedInventory(t *testing.T) {
	handler := newHandler(fake.NewHealthy())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/disks", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d; body = %s", got, want, recorder.Body.String())
	}
	const want = "{\"observedAt\":\"2026-10-06T00:00:00Z\",\"items\":[{\"id\":\"disk:fake-data-01\",\"model\":\"A-NAS Fake HDD\",\"transport\":\"sata\",\"capacityBytes\":512000000000,\"rotational\":true,\"role\":\"unassigned\",\"health\":\"healthy\",\"temperatureCelsius\":31},{\"id\":\"disk:fake-system-01\",\"model\":\"A-NAS Fake SSD\",\"transport\":\"nvme\",\"capacityBytes\":125000000000,\"rotational\":false,\"role\":\"system\",\"health\":\"healthy\",\"temperatureCelsius\":36}]}\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestHostStateEndpointReturnsOneObservedSnapshot(t *testing.T) {
	handler := newHandler(fake.NewHealthy())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/host-state", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d; body = %s", got, want, recorder.Body.String())
	}
	const want = "{\"dataSource\":\"simulated\",\"productVersion\":\"test-version\",\"observedAt\":\"2026-10-06T00:00:00Z\",\"system\":{\"id\":\"host:fake-01\",\"hostname\":\"anas-fake\",\"operatingSystem\":{\"name\":\"Debian\",\"version\":\"13\"},\"architecture\":\"amd64\",\"uptimeSeconds\":3600,\"health\":\"healthy\"},\"disks\":[{\"id\":\"disk:fake-data-01\",\"model\":\"A-NAS Fake HDD\",\"transport\":\"sata\",\"capacityBytes\":512000000000,\"rotational\":true,\"role\":\"unassigned\",\"health\":\"healthy\",\"temperatureCelsius\":31},{\"id\":\"disk:fake-system-01\",\"model\":\"A-NAS Fake SSD\",\"transport\":\"nvme\",\"capacityBytes\":125000000000,\"rotational\":false,\"role\":\"system\",\"health\":\"healthy\",\"temperatureCelsius\":36}]}\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestMetricsEndpointReturnsUncachedUtilisation(t *testing.T) {
	handler := newHandler(fake.NewHealthy())
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d; body = %s", got, want, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	const want = "{\"dataSource\":\"simulated\",\"observedAt\":\"2026-10-06T00:00:00Z\",\"cpu\":{\"usagePercent\":23.5,\"logicalCores\":4},\"memory\":{\"totalBytes\":8589934592,\"usedBytes\":3435973837},\"network\":{\"receiveBytesPerSecond\":2516582,\"transmitBytesPerSecond\":327680}}\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestMetricsEndpointHidesReaderFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	newHandler(fake.NewUnavailable()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))

	const want = "{\"error\":{\"code\":\"metrics_unavailable\",\"message\":\"host metrics are unavailable\"}}\n"
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != want {
		t.Fatalf("got %d %q, want 503 %q", recorder.Code, recorder.Body.String(), want)
	}
}

func TestStateEndpointsHideReaderFailure(t *testing.T) {
	for _, path := range []string{"/api/v1/system", "/api/v1/disks", "/api/v1/host-state"} {
		t.Run(path, func(t *testing.T) {
			handler := newHandler(fake.NewUnavailable())
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if got, want := recorder.Code, http.StatusServiceUnavailable; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			const want = "{\"error\":{\"code\":\"state_unavailable\",\"message\":\"host state is unavailable\"}}\n"
			if got := recorder.Body.String(); got != want {
				t.Fatalf("body = %q, want %q", got, want)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
		})
	}
}

func TestUnknownAPIPathReturnsJSONNotFound(t *testing.T) {
	handler := newHandler(fake.NewHealthy())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusNotFound; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	const want = "{\"error\":{\"code\":\"not_found\",\"message\":\"resource not found\"}}\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestKnownPathRejectsUnsupportedMethodAsJSON(t *testing.T) {
	handler := newHandler(fake.NewHealthy())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/system", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusMethodNotAllowed; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := recorder.Header().Get("Allow"), http.MethodGet; got != want {
		t.Fatalf("Allow = %q, want %q", got, want)
	}
	const want = "{\"error\":{\"code\":\"method_not_allowed\",\"message\":\"method not allowed\"}}\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHealthEndpointDoesNotDependOnHostState(t *testing.T) {
	handler := newHandler(fake.NewUnavailable())
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func newHandler(reader *fake.Reader) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httpapi.New(reader, httpapi.DataSourceSimulated, "test-version", logger)
}
