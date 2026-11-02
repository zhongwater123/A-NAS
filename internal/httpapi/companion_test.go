package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/companion"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// Exercise the real product guards, member scope and every companion operation
// against the same contract that generates the Web client.
func TestCompanionMemberScopeAndHandoff(t *testing.T) {
	f := newActivityFixture(t, func(s *photos.Service, a *accounts.Service) http.Handler {
		return localPhotos(t, s, serviceSessions{a})
	})
	f.client.do("GET", "/api/v1/companion", "", nil, 401)
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", 201)
	session, err := f.accounts.ResolveSession(context.Background(), owner.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.CreateMember(context.Background(), session.User, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	alice := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", 200)
	webID, desktopID := uuid.NewString(), uuid.NewString()
	claim := func(id, kind string, revision int64) string {
		b, _ := json.Marshal(companion.Claim{ClientID: id, Kind: kind, Revision: revision})
		return string(b)
	}
	decode := func(response []byte) companion.State {
		var state companion.State
		if err := json.Unmarshal(response, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	noCSRF := *owner
	noCSRF.csrf = ""
	noCSRF.do("POST", "/api/v1/companion/claim", claim(webID, "web", 0), nil, 403)
	state := decode(owner.do("POST", "/api/v1/companion/claim", claim(webID, "web", 0), nil, 200).Body.Bytes())
	holder := func(id string, epoch int64) string {
		b, _ := json.Marshal(companion.Holder{ClientID: id, Epoch: epoch})
		return string(b)
	}
	owner.do("POST", "/api/v1/companion/heartbeat", holder(webID, state.Active.Epoch), nil, 200)
	action := companion.Action{ClientID: webID, Epoch: state.Active.Epoch, EventID: uuid.NewString(), Type: "ball"}
	actionJSON, _ := json.Marshal(action)
	owner.do("POST", "/api/v1/companion/actions", string(actionJSON), nil, 200)
	// A different member cannot mutate even if they know the active client/epoch.
	alice.do("POST", "/api/v1/companion/actions", string(actionJSON), nil, 409)
	other := decode(alice.do("GET", "/api/v1/companion", "", nil, 200).Body.Bytes())
	conversation := companion.Action{ClientID: webID, Epoch: state.Active.Epoch, EventID: uuid.NewString(), Type: "conversation", ConversationID: "20261102_090000_abcdef"}
	conversationJSON, _ := json.Marshal(conversation)
	owner.do("POST", "/api/v1/companion/actions", string(conversationJSON), nil, 200)
	alice.do("POST", "/api/v1/companion/actions", string(conversationJSON), nil, 409)
	other = decode(alice.do("GET", "/api/v1/companion", "", nil, 200).Body.Bytes())
	if other.ConversationID != "" || other.PlayCount != 0 || other.Active != nil {
		t.Fatal("another member inherited progress")
	}
	state = decode(owner.do("GET", "/api/v1/companion", "", nil, 200).Body.Bytes())
	state = decode(owner.do("POST", "/api/v1/companion/claim", claim(desktopID, "desktop", state.Revision), nil, 200).Body.Bytes())
	if state.Active.ClientID != webID || state.Pending == nil || state.Pending.ClientID != desktopID {
		t.Fatal("candidate became active before acknowledgement")
	}
	oldEpoch := state.Active.Epoch
	state = decode(owner.do("POST", "/api/v1/companion/release", holder(webID, oldEpoch), nil, 200).Body.Bytes())
	if state.Active.ClientID != desktopID || state.Active.Epoch <= oldEpoch || state.PlayCount != 1 {
		t.Fatal("handoff lost state or fencing")
	}
	owner.do("POST", "/api/v1/companion/heartbeat", holder(webID, oldEpoch), nil, 409)
}
