package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/taskmanagerapi"
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
	const want = "{\"id\":\"host:fake-01\",\"hostname\":\"anas-fake\",\"operatingSystem\":{\"name\":\"Debian\",\"version\":\"13\"},\"architecture\":\"amd64\",\"uptimeSeconds\":3600,\"health\":\"healthy\",\"healthFindings\":[],\"productVersion\":\"test-version\",\"observedAt\":\"2026-10-06T00:00:00Z\",\"thermal\":{\"cpu\":{\"celsius\":46,\"hotCelsius\":90},\"gpus\":[{\"id\":\"gpu:fake-01\",\"name\":\"A-NAS Fake GPU\",\"integrated\":true}],\"fans\":[{\"id\":\"fan:fake-01\",\"number\":1,\"rpm\":1240,\"stopped\":false},{\"id\":\"fan:fake-02\",\"number\":2,\"rpm\":1860,\"stopped\":false}]}}\n"
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
	const want = "{\"observedAt\":\"2026-10-06T00:00:00Z\",\"items\":[{\"id\":\"disk:fake-data-01\",\"model\":\"A-NAS Fake HDD\",\"transport\":\"sata\",\"capacityBytes\":512000000000,\"rotational\":true,\"removable\":false,\"inUse\":false,\"filesystems\":[],\"role\":\"unassigned\",\"health\":\"healthy\",\"smartStatus\":\"healthy\",\"eligibleForDataVolume\":true,\"ineligibleReasons\":[],\"temperatureCelsius\":31,\"temperatureHotCelsius\":55},{\"id\":\"disk:fake-system-01\",\"model\":\"A-NAS Fake SSD\",\"transport\":\"nvme\",\"capacityBytes\":125000000000,\"rotational\":false,\"removable\":false,\"inUse\":true,\"filesystems\":[\"ext4\"],\"role\":\"system\",\"health\":\"healthy\",\"smartStatus\":\"healthy\",\"eligibleForDataVolume\":false,\"ineligibleReasons\":[\"system_disk\",\"in_use\"],\"temperatureCelsius\":36,\"temperatureHotCelsius\":70}]}\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestUnusableDisksStayVisibleAndIneligibleInThePublicContract(t *testing.T) {
	handler := contractChecked(t, newHandler(fake.NewWithUnusableDisks()))
	for _, path := range []string{"/api/v1/disks", "/api/v1/host-state", "/api/v1/system"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, recorder.Code, recorder.Body.String())
		}
		if path != "/api/v1/disks" {
			continue
		}
		var body struct {
			Items []struct {
				ID       string   `json:"id"`
				Eligible bool     `json:"eligibleForDataVolume"`
				Reasons  []string `json:"ineligibleReasons"`
			} `json:"items"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) != 5 {
			t.Fatalf("items = %+v", body.Items)
		}
		for _, disk := range body.Items {
			switch disk.ID {
			case "disk:fake-data-01":
				if !disk.Eligible {
					t.Fatal("healthy disk unavailable")
				}
			case "observation:empty-reader":
				if disk.Eligible || !slices.Contains(disk.Reasons, "missing_capacity") || !slices.Contains(disk.Reasons, "missing_identity") {
					t.Fatalf("empty reader = %+v", disk)
				}
			case "observation:duplicate-1", "observation:duplicate-2":
				if disk.Eligible || !slices.Contains(disk.Reasons, "duplicate_identity") {
					t.Fatalf("duplicate = %+v", disk)
				}
			}
		}
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
	const want = "{\"dataSource\":\"simulated\",\"productVersion\":\"test-version\",\"observedAt\":\"2026-10-06T00:00:00Z\",\"system\":{\"id\":\"host:fake-01\",\"hostname\":\"anas-fake\",\"operatingSystem\":{\"name\":\"Debian\",\"version\":\"13\"},\"architecture\":\"amd64\",\"uptimeSeconds\":3600,\"health\":\"healthy\",\"healthFindings\":[],\"thermal\":{\"cpu\":{\"celsius\":46,\"hotCelsius\":90},\"gpus\":[{\"id\":\"gpu:fake-01\",\"name\":\"A-NAS Fake GPU\",\"integrated\":true}],\"fans\":[{\"id\":\"fan:fake-01\",\"number\":1,\"rpm\":1240,\"stopped\":false},{\"id\":\"fan:fake-02\",\"number\":2,\"rpm\":1860,\"stopped\":false}]}},\"disks\":[{\"id\":\"disk:fake-data-01\",\"model\":\"A-NAS Fake HDD\",\"transport\":\"sata\",\"capacityBytes\":512000000000,\"rotational\":true,\"removable\":false,\"inUse\":false,\"filesystems\":[],\"role\":\"unassigned\",\"health\":\"healthy\",\"smartStatus\":\"healthy\",\"eligibleForDataVolume\":true,\"ineligibleReasons\":[],\"temperatureCelsius\":31,\"temperatureHotCelsius\":55},{\"id\":\"disk:fake-system-01\",\"model\":\"A-NAS Fake SSD\",\"transport\":\"nvme\",\"capacityBytes\":125000000000,\"rotational\":false,\"removable\":false,\"inUse\":true,\"filesystems\":[\"ext4\"],\"role\":\"system\",\"health\":\"healthy\",\"smartStatus\":\"healthy\",\"eligibleForDataVolume\":false,\"ineligibleReasons\":[\"system_disk\",\"in_use\"],\"temperatureCelsius\":36,\"temperatureHotCelsius\":70}]}\n"
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

// newHandler serves the host state and the task manager without asking for a
// session, as the product does once a request passed its guard.
func newHandler(reader *fake.Reader) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	for _, route := range taskmanagerapi.New(reader, string(httpapi.DataSourceSimulated), logger).Routes() {
		mux.HandleFunc(route.Pattern, func(w http.ResponseWriter, r *http.Request) { route.Handler(w, r, accounts.Session{}) })
	}
	mux.Handle("/", httpapi.New(reader, httpapi.DataSourceSimulated, "test-version", logger))
	return mux
}
