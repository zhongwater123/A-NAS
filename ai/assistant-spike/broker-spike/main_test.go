package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/user"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

type testMembers struct{}

func (testMembers) ResolveSessionIdentity(_ context.Context, token string) (accountid.Identity, error) {
	if token == "alice" || token == "bob" {
		return accountid.Identity{Username: token, Enabled: true}, nil
	}
	return accountid.Identity{}, accountid.ErrSessionNotFound
}

// fakeBroker answers as the File Broker would for alice's changes, and records
// what it was asked.
type fakeBroker struct {
	sessions  []string
	arguments string
	sessionID string
	requestID string
	cards     map[string]string
	refuse    *wire.Error
	next      int
}

func (f *fakeBroker) signedIn(ctx context.Context) error {
	token := accountid.SessionToken(ctx)
	f.sessions = append(f.sessions, token)
	if token == "" {
		return fmt.Errorf("%w: no session for file operation", filesystem.ErrForbidden)
	}
	if _, err := (testMembers{}).ResolveSessionIdentity(ctx, token); err != nil {
		return &wire.Error{Code: wire.CodeUnauthenticated}
	}
	return nil
}

func (f *fakeBroker) PrepareChange(ctx context.Context, sessionID, requestID string, arguments json.RawMessage) (json.RawMessage, error) {
	if err := f.signedIn(ctx); err != nil {
		return nil, err
	}
	if f.refuse != nil {
		return nil, f.refuse
	}
	f.arguments, f.sessionID, f.requestID = string(arguments), sessionID, requestID
	f.next++
	id := fmt.Sprintf("0000000%d", f.next)
	f.cards[id] = fmt.Sprintf(`{"id":%q,"status":"awaiting"}`, id)
	return json.RawMessage(f.cards[id]), nil
}

func (f *fakeBroker) DecideChange(ctx context.Context, id, decision string) (json.RawMessage, error) {
	if err := f.signedIn(ctx); err != nil {
		return nil, err
	}
	if f.refuse != nil {
		return nil, f.refuse
	}
	f.cards[id] = fmt.Sprintf(`{"id":%q,"status":%q}`, id, decision)
	return json.RawMessage(f.cards[id]), nil
}

func (f *fakeBroker) GetChange(ctx context.Context, id string) (json.RawMessage, error) {
	if err := f.signedIn(ctx); err != nil {
		return nil, err
	}
	card, ok := f.cards[id]
	if !ok {
		return nil, &wire.Error{Code: wire.CodeNotFound}
	}
	return json.RawMessage(card), nil
}

func call(t *testing.T, handler http.Handler, method, target, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

func TestProductProtocolSignsInAsTheAccountAndPassesTheArguments(t *testing.T) {
	broker := &fakeBroker{cards: map[string]string{}}
	handler := newProduct(broker, testMembers{})
	body := `{"summary":"复制","operations":[{"op":"copy","path":"/nas/个人空间/a","source":"/nas/助手/a"}],` +
		`"sessionId":"s1","runId":"r1","requestId":"q1","extra":1}`
	status, card := call(t, handler, http.MethodPost, "/changes/prepare?account=alice", body)
	if status != http.StatusOK || !strings.Contains(card, `"00000001"`) {
		t.Fatalf("prepare = %d %s", status, card)
	}
	want := `{"operations":[{"op":"copy","path":"/nas/个人空间/a","source":"/nas/助手/a"}],"summary":"复制"}`
	if broker.arguments != want || broker.sessionID != "s1" || broker.requestID != "q1" {
		t.Fatalf("passed %s, session %q, request %q", broker.arguments, broker.sessionID, broker.requestID)
	}
	if status, card := call(t, handler, http.MethodPost, "/changes/00000001/approve?account=alice", ""); status != http.StatusOK ||
		!strings.Contains(card, `"approve"`) {
		t.Fatalf("approve = %d %s", status, card)
	}
	call(t, handler, http.MethodPost, "/changes/prepare?account=alice", `{"summary":"x","operations":[]}`)
	call(t, handler, http.MethodPost, "/changes/prepare?account=alice", `{"summary":"y","operations":[]}`)
	// A change the File Broker no longer keeps is left out of the list.
	delete(broker.cards, "00000002")
	ids := func(account string) (int, []string) {
		status, page := call(t, handler, http.MethodGet, "/changes?account="+account, "")
		var listed struct {
			Changes []struct {
				ID string `json:"id"`
			} `json:"changes"`
		}
		if err := json.Unmarshal([]byte(page), &listed); err != nil {
			t.Fatalf("list = %d %s", status, page)
		}
		var got []string
		for _, c := range listed.Changes {
			got = append(got, c.ID)
		}
		return status, got
	}
	if status, got := ids("alice"); status != http.StatusOK || strings.Join(got, ",") != "00000003,00000001" {
		t.Fatalf("alice's list = %d %v, want the kept ones newest first", status, got)
	}
	if status, got := ids("bob"); status != http.StatusOK || len(got) != 0 {
		t.Fatalf("bob's list = %d %v, want none of alice's", status, got)
	}
	for _, target := range []string{"/changes", "/changes?account=mallory"} {
		status, body := call(t, handler, http.MethodGet, target, "")
		if status != http.StatusUnauthorized || !strings.Contains(body, "unauthenticated") {
			t.Errorf("GET %s = %d %s, want 401 unauthenticated", target, status, body)
		}
	}
	for _, token := range broker.sessions {
		if token != "alice" {
			t.Fatalf("signed in as %q", token)
		}
	}
}

func TestProductProtocolStatuses(t *testing.T) {
	for name, c := range map[string]struct {
		refuse *wire.Error
		target string
		status int
		said   string
	}{
		"refused submission": {&wire.Error{Code: wire.CodeChangeInvalid, Message: "第 1 项：/nas/个人空间/.a-nas-trash：不能更改系统文件夹"},
			"/changes/prepare?account=alice", http.StatusUnprocessableEntity, "不能更改系统文件夹"},
		"decided already":  {&wire.Error{Code: wire.CodeChangeDecided}, "/changes/00000001/approve?account=alice", http.StatusConflict, "change_decided"},
		"not a member":     {nil, "/changes/prepare?account=mallory", http.StatusUnauthorized, "unauthenticated"},
		"no account":       {nil, "/changes/prepare", http.StatusUnauthorized, "unauthenticated"},
		"unknown decision": {nil, "/changes/00000001/apply?account=alice", http.StatusNotFound, "not found"},
	} {
		t.Run(name, func(t *testing.T) {
			handler := newProduct(&fakeBroker{cards: map[string]string{}, refuse: c.refuse}, testMembers{})
			status, body := call(t, handler, http.MethodPost, c.target, `{"summary":"x","operations":[]}`)
			if status != c.status || !strings.Contains(body, c.said) {
				t.Fatalf("= %d %s, want %d with %q", status, body, c.status, c.said)
			}
		})
	}
	handler := newProduct(&fakeBroker{cards: map[string]string{}}, testMembers{})
	if status, body := call(t, handler, http.MethodGet, "/changes/00000009?account=alice", ""); status != http.StatusNotFound {
		t.Fatalf("an unknown change = %d %s", status, body)
	}
}

func TestIdentityIsAnANASAccountInItsGroups(t *testing.T) {
	member := &user.User{Username: "alice", Uid: "20100", Gid: "20100"}
	for name, c := range map[string]struct {
		account *user.User
		groups  []string
		role    accountid.Role
		ok      bool
	}{
		"member":             {member, []string{"20100", "20000"}, accountid.RoleMember, true},
		"administrator":      {member, []string{"20100", "20000", "20001"}, accountid.RoleAdmin, true},
		"not in a-nas-users": {member, []string{"20100"}, "", false},
		"root":               {&user.User{Username: "root", Uid: "0", Gid: "0"}, []string{"0", "20000"}, "", false},
		"a system account":   {&user.User{Username: "a-nas", Uid: "999", Gid: "999"}, []string{"999", "20000"}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := identity(c.account, c.groups)
			if (err == nil) != c.ok || (c.ok && (got.Role != c.role || got.UID != 20100 || got.Username != "alice" || !got.Enabled)) {
				t.Fatalf("identity = %+v, %v", got, err)
			}
		})
	}
}
