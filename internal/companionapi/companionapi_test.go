package companionapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/companion"
	"github.com/zhongwater123/A-NAS/internal/openapitest"
)

func TestRoutePosturesAndMemberOnlyContract(t *testing.T) {
	store, err := companion.Open(filepath.Join(t.TempDir(), "companion.db"), companion.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	api := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	routes := api.Routes()
	for _, route := range routes {
		want := apiroute.Mutation
		if route.Method == http.MethodGet {
			want = apiroute.Session
		}
		if route.Posture != want {
			t.Errorf("%s %s posture=%v", route.Method, route.Pattern, route.Posture)
		}
	}
	session := accounts.Session{User: accounts.User{ID: "alice"}}
	body := `{"clientId":"` + uuid.NewString() + `","kind":"web","revision":0}`
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/companion/claim", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", "test")
	recorder := httptest.NewRecorder()
	routes[1].Handler(recorder, request, session)
	if recorder.Code != 200 {
		t.Fatalf("claim: %d %s", recorder.Code, recorder.Body.String())
	}
	check := httptest.NewRequest(http.MethodPost, request.URL.String(), strings.NewReader(body))
	check.Header = request.Header.Clone()
	openapitest.Check(t, check, recorder)
	bob, _ := store.Read(context.Background(), "bob")
	if bob.Active != nil {
		t.Fatal("owner accepted through request data")
	}
	for _, body := range []string{`{}`, `{"clientId":null,"kind":"web","revision":0}`, `{"owner":"bob","clientId":"x","kind":"web","revision":0}`} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/companion/claim", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		routes[1].Handler(w, r, session)
		if w.Code != 400 {
			t.Errorf("invalid claim %s: %d", body, w.Code)
		}
	}
}

func TestEveryOperationKeepsTheGeneratedContract(t *testing.T) {
	store, err := companion.Open(filepath.Join(t.TempDir(), "companion.db"), companion.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	routes := New(store, nil).Routes()
	session := accounts.Session{User: accounts.User{ID: "alice"}}
	call := func(index int, body any) companion.State {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		makeRequest := func() *http.Request {
			request := httptest.NewRequest(routes[index].Method, "http://127.0.0.1:8080"+routes[index].Pattern, bytes.NewReader(encoded))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", "test")
			return request
		}
		w := httptest.NewRecorder()
		routes[index].Handler(w, makeRequest(), session)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", routes[index].Pattern, w.Code, w.Body.String())
		}
		openapitest.Check(t, makeRequest(), w)
		var state companion.State
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	webID, desktopID := uuid.NewString(), uuid.NewString()
	state := call(0, nil)
	state = call(1, companion.Claim{ClientID: webID, Kind: "web", Revision: state.Revision})
	state = call(2, companion.Holder{ClientID: webID, Epoch: state.Active.Epoch})
	state = call(4, companion.Action{ClientID: webID, Epoch: state.Active.Epoch, EventID: uuid.NewString(), Type: "ball"})
	state = call(4, companion.Action{ClientID: webID, Epoch: state.Active.Epoch, EventID: uuid.NewString(), Type: "conversation", ConversationID: "20261102_090000_abcdef"})
	if state.ConversationID != "20261102_090000_abcdef" {
		t.Fatal("missing shared conversation")
	}
	oldEpoch := state.Active.Epoch
	state = call(1, companion.Claim{ClientID: desktopID, Kind: "desktop", Revision: state.Revision})
	if state.Active.ClientID != webID || state.Pending.ClientID != desktopID {
		t.Fatal("candidate did not wait")
	}
	state = call(3, companion.Holder{ClientID: webID, Epoch: oldEpoch})
	if state.Active.ClientID != desktopID || state.PlayCount != 1 || state.Active.Epoch <= oldEpoch {
		t.Fatal("handoff lost progress or fencing")
	}
	state = call(4, companion.Action{ClientID: desktopID, Epoch: state.Active.Epoch, EventID: uuid.NewString(), Type: "character", Character: "chestnut"})
	if state.Character != "chestnut" {
		t.Fatal("character preference was not shared")
	}
	state = call(3, companion.Holder{ClientID: desktopID, Epoch: state.Active.Epoch})
	if state.Active != nil {
		t.Fatal("release without candidate did not return home")
	}
}
