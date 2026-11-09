package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filebroker"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

type changesHarness struct {
	volume   string
	server   *Server
	client   *filebroker.Client
	accounts *uidDirectory
}

var alice = accountid.Identity{Username: "alice", UID: 20101, Role: accountid.RoleMember, Enabled: true}

var aliceAccount = accountid.SessionUser{UserID: "user-alice", Username: "alice", Role: accountid.RoleMember}

// uidDirectory is the account directory the changes socket asks who is
// behind a UID.
type uidDirectory struct {
	mu   sync.Mutex
	uids map[int]accountid.SessionUser
}

func (d *uidDirectory) ResolveAccountUID(_ context.Context, uid int) (accountid.SessionUser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	user, ok := d.uids[uid]
	if !ok {
		return accountid.SessionUser{}, accountid.ErrUserNotFound
	}
	return user, nil
}

// add makes the directory name user at uid, whether or not the UID is in the
// A-NAS range.
func (d *uidDirectory) add(uid int, user accountid.SessionUser) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.uids[uid] = user
}

// newChangesHarness runs the broker with the executor over a volume laid out
// as the Host Agent does; workers run as the test user, the confined one
// under Landlock as in production.
func newChangesHarness(t *testing.T) *changesHarness {
	t.Helper()
	volume := t.TempDir()
	for _, dir := range []string{
		"spaces/private/alice/.a-nas-trash/alice", "spaces/shared/.a-nas-trash/alice", "spaces/shared/Agent空间/alice",
		"assistant/alice/files/待确认",
	} {
		if err := os.MkdirAll(filepath.Join(volume, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"spaces/private/alice/计划.md":      "# 计划\n",
		"assistant/alice/files/待确认/计划.md": "# 计划\n- 买牛奶\n",
		"spaces/shared/通知.md":             "全家\n",
	} {
		if err := os.WriteFile(filepath.Join(volume, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	accounts := &uidDirectory{uids: map[int]accountid.SessionUser{alice.UID: aliceAccount}}
	server, err := NewServer(Config{
		Sessions: resolverFunc(func(token string) (accountid.Identity, error) {
			return lookup(map[string]accountid.Identity{"alice-token": alice}, token)
		}),
		Accounts:   accounts,
		VolumeRoot: volume, VolumeReady: func() bool { return true },
		AssistantChanges: filepath.Join(t.TempDir(), "changes"),
		Command:          func(volumeRoot string) *exec.Cmd { return exec.Command(os.Args[0], WorkerArgument, volumeRoot) },
		LookupUID: func(username string) (int, error) {
			if username == "alice" {
				return 20101, nil
			}
			return 0, errors.New("unknown user")
		},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		skipCredentials: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(shortTempDir(t), "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = listener.Close()
		server.Close()
	})
	return &changesHarness{volume: volume, server: server, client: filebroker.NewClient(socket), accounts: accounts}
}

const editPlan = `{"summary":"计划加一项","operations":[{"op":"write","path":"/nas/个人空间/计划.md","source":"/nas/助手/待确认/计划.md"}]}`

func TestConfinedWorkerDoesNotFollowWritableDirectoryLinks(t *testing.T) {
	for _, parent := range []bool{false, true} {
		name := "member directory"
		if parent {
			name = "ancestor directory"
		}
		t.Run(name, func(t *testing.T) {
			h := newChangesHarness(t)
			shared := filepath.Join(h.volume, "spaces/shared")
			link := filepath.Join(shared, "Agent空间/alice")
			outside := shared
			if parent {
				link = filepath.Join(shared, "Agent空间")
				outside = filepath.Join(shared, "alice")
				if err := os.Mkdir(outside, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(link, link+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(shared, link); err != nil {
				t.Fatal(err)
			}
			member, err := h.server.memberFiles(alice)
			if err != nil {
				return // Refusing to start with an unsafe grant is fail-closed.
			}
			if err := member.Mkdir(context.Background(), filepath.Join(outside, "unauthorized")); err == nil {
				t.Fatal("a symlink expanded the confined worker's writable scope")
			}
		})
	}
}

func TestTheMembersConfirmedChangeIsAppliedByAConfinedWorker(t *testing.T) {
	h := newChangesHarness(t)
	session := accountid.WithSessionToken(context.Background(), "alice-token")
	card, err := h.client.PrepareChange(session, "0a1b2c3d", "req-1", json.RawMessage(editPlan))
	if err != nil {
		t.Fatal(err)
	}
	var change struct {
		ID         string `json:"id"`
		Operations []struct {
			After string `json:"after"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(card, &change); err != nil || change.Operations[0].After != "# 计划\n- 买牛奶\n" {
		t.Fatalf("card = %s %v", card, err)
	}
	if _, err := h.client.DecideChange(session, change.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	// What the assistant socket does for alice's MCP tool.
	member, err := h.server.memberFiles(alice)
	if err != nil {
		t.Fatal(err)
	}
	if result := h.server.changes.Apply(context.Background(), "alice", member, []byte(editPlan)); !strings.Contains(result, "已执行") {
		t.Fatalf("apply = %s", result)
	}
	if data, _ := os.ReadFile(filepath.Join(h.volume, "spaces/private/alice/计划.md")); string(data) != "# 计划\n- 买牛奶\n" {
		t.Fatalf("plan = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(h.volume, "spaces/private/alice/.a-nas-trash/alice/计划.md")); string(data) != "# 计划\n" {
		t.Fatalf("old version = %q", data)
	}
	got, err := h.client.GetChange(session, change.ID)
	if err != nil || !strings.Contains(string(got), `"status":"applied"`) {
		t.Fatalf("change = %s %v", got, err)
	}
	if _, err := h.client.DecideChange(session, change.ID, "approve"); !errors.Is(err, filebroker.ErrChangeDecided) {
		t.Fatalf("deciding again: %v", err)
	}
}

func TestTheConfinedWorkerWritesOnlyTheMembersPlaces(t *testing.T) {
	h := newChangesHarness(t)
	member, err := h.server.memberFiles(alice)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := member.MkdirAll(ctx, filepath.Join(h.volume, "spaces/private/alice/新")); err != nil {
		t.Fatalf("writing alice's space: %v", err)
	}
	if err := member.MkdirAll(ctx, filepath.Join(h.volume, "spaces/shared/Agent空间/alice/新")); err != nil {
		t.Fatalf("writing her Agent空间 folder: %v", err)
	}
	// Even where the kernel ACL would let the member write, Landlock does not.
	for _, rel := range []string{"spaces/shared/新", "assistant/alice/files/新", "spaces/shared/Agent空间/新"} {
		if err := member.MkdirAll(ctx, filepath.Join(h.volume, rel)); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("mkdir %s: %v, want permission denied", rel, err)
		}
	}
	if err := member.Rename(ctx, filepath.Join(h.volume, "spaces/shared/通知.md"), filepath.Join(h.volume, "spaces/private/alice/通知.md")); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("moving a shared file into her space: %v, want permission denied", err)
	}
	if file, err := member.Open(ctx, filepath.Join(h.volume, "spaces/shared/通知.md")); err != nil {
		t.Errorf("reading the shared space: %v", err)
	} else {
		_ = file.Close()
	}
}

func TestChangesNeedTheMembersSession(t *testing.T) {
	h := newChangesHarness(t)
	if _, err := h.client.PrepareChange(accountid.WithSessionToken(context.Background(), "forged"), "", "", json.RawMessage(editPlan)); !errors.Is(err, filesystem.ErrForbidden) {
		t.Fatalf("prepare with a forged session: %v", err)
	}
	if _, err := h.client.DecideChange(accountid.WithSessionToken(context.Background(), "forged"), "deadbeef", "approve"); !errors.Is(err, filesystem.ErrForbidden) {
		t.Fatalf("approve with a forged session: %v", err)
	}
	session := accountid.WithSessionToken(context.Background(), "alice-token")
	if _, err := h.client.PrepareChange(session, "", "", json.RawMessage(`{"operations":[{"op":"delete","path":"/nas/共享空间/通知.md"}]}`)); !errors.Is(err, filebroker.ErrChangeInvalid) {
		t.Fatalf("prepare outside her places: %v", err)
	}
	if _, err := h.client.GetChange(session, "deadbeef"); !errors.Is(err, filebroker.ErrChangeNotFound) {
		t.Fatalf("get a missing change: %v", err)
	}
}

func TestTheAssistantSocketServesOnlyMembersByTheirUID(t *testing.T) {
	uid := os.Getuid()
	if uid >= accountid.FirstUserUID && uid <= accountid.LastUserUID {
		t.Skipf("the test runs as UID %d, inside the A-NAS range", uid)
	}
	h := newChangesHarness(t)
	// Even a UID the directory names is refused outside the A-NAS range.
	h.accounts.add(uid, aliceAccount)
	socket := filepath.Join(shortTempDir(t), "changes.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = h.server.ServeAssistantChanges(listener) }()
	t.Cleanup(func() { _ = listener.Close() })
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}}
	// The test runs as a UID outside the A-NAS range, as root and system services do.
	response, err := client.Post("http://changes/apply", "application/json", strings.NewReader(editPlan))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("apply from a non-member UID = %d", response.StatusCode)
	}
	if content, err := os.ReadFile(filepath.Join(h.volume, "spaces/private/alice/计划.md")); err != nil || string(content) != "# 计划\n" {
		t.Fatalf("the plan was applied: %q, %v", content, err)
	}
}

// The changes socket serves exactly the A-NAS UID range: a member's UID is
// served as that member, and a UID outside the range is refused before the
// directory is asked, even one it would name.
func TestTheAssistantSocketChecksTheUIDRange(t *testing.T) {
	h := newChangesHarness(t)
	for uid, want := range map[int]int{
		0:                          http.StatusForbidden,
		1000:                       http.StatusForbidden,
		accountid.FirstUserUID - 1: http.StatusForbidden,
		accountid.FirstUserUID:     http.StatusOK,
		alice.UID:                  http.StatusOK,
		accountid.LastUserUID:      http.StatusOK,
		accountid.LastUserUID + 1:  http.StatusForbidden,
	} {
		h.accounts.add(uid, aliceAccount)
		var served *accountid.Identity
		handler := h.server.withAssistant(func(_ http.ResponseWriter, _ *http.Request, identity accountid.Identity) {
			served = &identity
		})
		recorder := httptest.NewRecorder()
		handler(recorder, httptest.NewRequestWithContext(context.WithValue(t.Context(), peerKey{}, uid), http.MethodGet, "http://changes/recent", nil))
		if recorder.Code != want {
			t.Errorf("UID %d: status %d, want %d", uid, recorder.Code, want)
		}
		if want == http.StatusOK && (served == nil || served.UID != uid || served.Username != "alice") {
			t.Errorf("UID %d served as %+v, want alice at that UID", uid, served)
		}
		if want != http.StatusOK && served != nil {
			t.Errorf("UID %d reached the handler as %+v", uid, served)
		}
	}
}
