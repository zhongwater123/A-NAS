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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filebroker"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

type changesHarness struct {
	volume string
	server *Server
	client *filebroker.Client
}

var alice = accountid.Identity{Username: "alice", UID: 20101, Role: accountid.RoleMember, Enabled: true}

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
	server, err := NewServer(Config{
		Sessions: resolverFunc(func(token string) (accountid.Identity, error) {
			return lookup(map[string]accountid.Identity{"alice-token": alice}, token)
		}),
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
	return &changesHarness{volume: volume, server: server, client: filebroker.NewClient(socket)}
}

const editPlan = `{"summary":"计划加一项","operations":[{"op":"write","path":"/nas/个人空间/计划.md","source":"/nas/助手/待确认/计划.md"}]}`

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
	h := newChangesHarness(t)
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
}
