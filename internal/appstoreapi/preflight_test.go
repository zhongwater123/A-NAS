package appstoreapi_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
)

type identityCountingHost struct {
	recordingHost
	identities int
}

type rejectingPreparationStore struct {
	appstore.Store
	rejection error
}

func (s rejectingPreparationStore) ReserveInstall(context.Context, string, string, appstore.Identity) (appstore.InstallReservation, error) {
	return appstore.InstallReservation{}, s.rejection
}

func TestRejectedReservationsDoNotPrepareTheHost(t *testing.T) {
	for _, rejection := range []error{appstore.ErrBusy, appstore.ErrPortInUse, appstore.ErrNameInUse, appstore.ErrAlreadyInstalled, appstore.ErrUnavailable} {
		t.Run(rejection.Error(), func(t *testing.T) {
			host := &recordingHost{}
			store := rejectingPreparationStore{Store: newStore(t), rejection: rejection}
			handler := appstoreapi.New(store, appstoreapi.DataSourceSimulated, appstoreapi.Options{Host: host, Logger: discard()})
			var plan struct{ Digest string }
			decode(t, serve(handler, http.MethodGet, "/api/v1/apps/navidrome/plan", "", nil), http.StatusOK, &plan)
			response := serve(handler, http.MethodPost, "/api/v1/apps/navidrome/install", `{"digest":"`+plan.Digest+`"}`, http.Header{"Content-Type": {"application/json"}})
			if response.Code < 400 {
				t.Fatalf("rejection: %d %s", response.Code, response.Body)
			}
			if len(host.prepared) != 0 {
				t.Fatalf("rejected reservation prepared host: %+v", host.prepared)
			}
		})
	}
}

type failingPreparationHost struct{ recordingHost }

func (*failingPreparationHost) PrepareApp(context.Context, string, []string, bool) error {
	return errors.New("data volume lost")
}

func TestFailedPreparationCancelsTheAcceptedInstall(t *testing.T) {
	store := newStore(t)
	host := &failingPreparationHost{}
	handler := appstoreapi.New(store, appstoreapi.DataSourceSimulated, appstoreapi.Options{Host: host, Logger: discard()})
	var plan struct{ Digest string }
	decode(t, serve(handler, http.MethodGet, "/api/v1/apps/navidrome/plan", "", nil), http.StatusOK, &plan)
	response := serve(handler, http.MethodPost, "/api/v1/apps/navidrome/install", `{"digest":"`+plan.Digest+`"}`, http.Header{"Content-Type": {"application/json"}})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("prepare failure: %d %s", response.Code, response.Body)
	}
	store.Wait()
	apps, err := store.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.ID == "navidrome" && (app.Total != 0 || app.Job == nil || app.Job.State != appstore.JobFailed) {
			t.Fatalf("failed preparation: %+v", app)
		}
	}
	// The cancelled job must release the slot for the next install.
	identity, err := host.AppIdentity(context.Background(), "navidrome")
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveInstall(context.Background(), "navidrome", plan.Digest, identity)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.CancelInstall(context.Background(), "navidrome", reservation.Token)
	store.Wait()
}

func (h *identityCountingHost) AppIdentity(ctx context.Context, id string) (appid.Identity, error) {
	h.identities++
	return h.recordingHost.AppIdentity(ctx, id)
}

func TestUnknownAppDoesNotReserveAnIdentity(t *testing.T) {
	for _, operation := range []string{"plan", "install"} {
		t.Run(operation, func(t *testing.T) {
			host := &identityCountingHost{}
			handler := appstoreapi.New(newStore(t), appstoreapi.DataSourceSimulated, appstoreapi.Options{Host: host, Logger: discard()})
			method := http.MethodGet
			if operation == "install" {
				method = http.MethodPost
			}
			response := serve(handler, method, "/api/v1/apps/not-in-catalog/"+operation, `{"digest":"`+strings.Repeat("a", 64)+`"}`, http.Header{"Content-Type": {"application/json"}})
			if response.Code != http.StatusNotFound {
				t.Fatalf("response: %d %s", response.Code, response.Body)
			}
			if host.identities != 0 {
				t.Fatalf("unknown app reserved %d identities", host.identities)
			}
		})
	}
}

func TestRejectedReinstallDoesNotPrepareTheHost(t *testing.T) {
	store := newStore(t)
	host := &recordingHost{}
	handler := appstoreapi.New(store, appstoreapi.DataSourceSimulated, appstoreapi.Options{Host: host, Logger: discard()})
	var plan struct{ Digest string }
	decode(t, serve(handler, http.MethodGet, "/api/v1/apps/navidrome/plan", "", nil), http.StatusOK, &plan)
	request := `{"digest":"` + plan.Digest + `"}`
	headers := http.Header{"Content-Type": {"application/json"}}
	response := serve(handler, http.MethodPost, "/api/v1/apps/navidrome/install", request, headers)
	if response.Code != http.StatusAccepted {
		t.Fatalf("install: %d %s", response.Code, response.Body)
	}
	store.Wait()
	host.prepared = nil
	response = serve(handler, http.MethodPost, "/api/v1/apps/navidrome/install", request, headers)
	if response.Code != http.StatusConflict {
		t.Fatalf("reinstall: %d %s", response.Code, response.Body)
	}
	if len(host.prepared) != 0 {
		t.Fatalf("rejected install prepared host: %+v", host.prepared)
	}
}
