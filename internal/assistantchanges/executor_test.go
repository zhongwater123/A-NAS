package assistantchanges

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/files"
)

type fixture struct {
	t      *testing.T
	volume string
	member files.FileSystem
	exec   *Executor
	clock  time.Time
}

// newFixture lays out alice's spaces, recycle bins, Agent空间 folder and
// staging area as the Host Agent does; the member's file system is the same
// no-follow one the File Broker's workers use.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	volume := t.TempDir()
	for _, dir := range []string{
		"spaces/private/alice/.a-nas-trash/alice", "spaces/private/bob", "spaces/shared/.a-nas-trash/alice",
		"spaces/shared/Agent空间/alice", "spaces/shared/Agent空间/bob", "assistant/alice/files/待确认",
	} {
		if err := os.MkdirAll(filepath.Join(volume, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, volume: volume, member: files.NewRootFileSystem(volume, false), clock: time.Unix(1_800_000_000, 0)}
	f.exec = &Executor{Root: filepath.Join(t.TempDir(), "changes"), Volume: volume, Now: func() time.Time { return f.clock }}
	f.put("spaces/private/alice/计划.md", "# 计划\n- 买菜\n")
	return f
}

func (f *fixture) put(rel, content string) {
	f.t.Helper()
	name := filepath.Join(f.volume, rel)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) get(rel string) string {
	data, err := os.ReadFile(filepath.Join(f.volume, rel))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(data)
}

func (f *fixture) prepare(submission string) (map[string]any, error) {
	f.t.Helper()
	raw, err := f.exec.Prepare(context.Background(), "alice", f.member, []byte(submission), "0a1b2c3d", "req-1")
	if err != nil {
		return nil, err
	}
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		f.t.Fatal(err)
	}
	return card, nil
}

func (f *fixture) approve(submission string) string {
	f.t.Helper()
	card, err := f.prepare(submission)
	if err != nil {
		f.t.Fatalf("prepare: %v", err)
	}
	id := card["id"].(string)
	if _, err := f.exec.Decide("alice", id, "approve"); err != nil {
		f.t.Fatal(err)
	}
	return id
}

const editPlan = `{"summary":"计划加一项","operations":[{"op":"write","path":"/nas/个人空间/计划.md","source":"/nas/助手/待确认/计划.md"}]}`

func TestAnApprovedWriteIsAppliedOnceFromTheCopyTheMemberSaw(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", "# 计划\n- 买菜\n- 买牛奶\n")
	card, err := f.prepare(editPlan)
	if err != nil {
		t.Fatal(err)
	}
	op := card["operations"].([]any)[0].(map[string]any)
	if op["before"] != "# 计划\n- 买菜\n" || op["after"] != "# 计划\n- 买菜\n- 买牛奶\n" || op["exists"] != true {
		t.Fatalf("card = %v", card)
	}
	if _, err := f.exec.Decide("alice", card["id"].(string), "approve"); err != nil {
		t.Fatal(err)
	}
	// The draft changes after the card: what the member saw is written.
	f.put("assistant/alice/files/待确认/计划.md", "# 被改写\n")
	result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan))
	if !strings.Contains(result, "1 项更改已执行") {
		t.Fatalf("apply = %s", result)
	}
	if got := f.get("spaces/private/alice/计划.md"); got != "# 计划\n- 买菜\n- 买牛奶\n" {
		t.Fatalf("plan = %q", got)
	}
	if got := f.get("spaces/private/alice/.a-nas-trash/alice/计划.md"); got != "# 计划\n- 买菜\n" {
		t.Fatalf("old version in the recycle bin = %q", got)
	}
	if again := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan)); !strings.Contains(again, "没有得到用户本人的确认") {
		t.Fatalf("second apply = %s", again)
	}
	status, _ := f.exec.Get("alice", card["id"].(string))
	if !strings.Contains(string(status), `"status":"applied"`) {
		t.Fatalf("change = %s", status)
	}
}

func TestNothingIsAppliedWithoutAFreshGrant(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", "新内容\n")
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan)); !strings.Contains(result, "没有得到用户本人的确认") {
		t.Fatalf("apply without a card = %s", result)
	}
	card, _ := f.prepare(editPlan)
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan)); !strings.Contains(result, "没有得到用户本人的确认") {
		t.Fatalf("apply before approval = %s", result)
	}
	if _, err := f.exec.Decide("alice", card["id"].(string), "reject"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec.Decide("alice", card["id"].(string), "approve"); !errors.Is(err, ErrDecided) {
		t.Fatalf("approving a rejected change: %v", err)
	}
	f.approve(editPlan)
	f.clock = f.clock.Add(GrantTTL + time.Second)
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan)); !strings.Contains(result, "没有得到用户本人的确认") {
		t.Fatalf("apply after the grant expired = %s", result)
	}
	// Another member's assistant with the same arguments has no grant.
	f.clock = f.clock.Add(-GrantTTL)
	f.approve(editPlan)
	// (His submission names his own staging area, so it is refused before any grant.)
	if result := f.exec.Apply(context.Background(), "bob", f.member, []byte(editPlan)); !strings.Contains(result, "什么都没有改变") {
		t.Fatalf("bob applying alice's grant = %s", result)
	}
	if got := f.get("spaces/private/alice/计划.md"); got != "# 计划\n- 买菜\n" {
		t.Fatalf("plan changed: %q", got)
	}
	// An expired grant is void even before its time is up.
	id := f.approve(`{"summary":"x","operations":[{"op":"mkdir","path":"/nas/个人空间/新"}]}`)
	if _, err := f.exec.Decide("alice", id, "expire"); err != nil {
		t.Fatal(err)
	}
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(`{"summary":"x","operations":[{"op":"mkdir","path":"/nas/个人空间/新"}]}`)); !strings.Contains(result, "没有得到") {
		t.Fatalf("apply after expiry = %s", result)
	}
}

func TestATargetChangedAfterTheCardIsNotOverwritten(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", "助手的版本\n")
	f.approve(editPlan)
	time.Sleep(10 * time.Millisecond)
	f.put("spaces/private/alice/计划.md", "成员自己改的\n")
	result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan))
	if !strings.Contains(result, "被改动过") || !strings.Contains(result, "什么都没有改变") {
		t.Fatalf("apply = %s", result)
	}
	if got := f.get("spaces/private/alice/计划.md"); got != "成员自己改的\n" {
		t.Fatalf("plan = %q", got)
	}
}

func TestSubmissionsOutsideTheMembersPlacesAreRefused(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/x.md", "x\n")
	f.put("spaces/shared/通知.md", "全家\n")
	for name, submission := range map[string]string{
		"dot dot":              `{"operations":[{"op":"delete","path":"/nas/个人空间/../共享空间/通知.md"}]}`,
		"shared root":          `{"operations":[{"op":"write","path":"/nas/共享空间/通知.md","source":"/nas/助手/待确认/x.md"}]}`,
		"another member":       `{"operations":[{"op":"write","path":"/nas/共享空间/Agent空间/bob/x.md","source":"/nas/助手/待确认/x.md"}]}`,
		"the space itself":     `{"operations":[{"op":"delete","path":"/nas/个人空间"}]}`,
		"recycle bin":          `{"operations":[{"op":"mkdir","path":"/nas/个人空间/.a-nas-trash/alice/x"}]}`,
		"source outside":       `{"operations":[{"op":"write","path":"/nas/个人空间/x.md","source":"/nas/共享空间/通知.md"}]}`,
		"unknown operation":    `{"operations":[{"op":"chmod","path":"/nas/个人空间/计划.md"}]}`,
		"nothing":              `{"operations":[]}`,
		"missing file":         `{"operations":[{"op":"delete","path":"/nas/个人空间/没有.md"}]}`,
		"move onto a file":     `{"operations":[{"op":"move","path":"/nas/个人空间/计划.md","to":"/nas/个人空间/计划.md"}]}`,
		"source not yet there": `{"operations":[{"op":"write","path":"/nas/个人空间/y.md","source":"/nas/助手/待确认/y.md"}]}`,
	} {
		if _, err := f.prepare(submission); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: prepare error = %v, want ErrInvalid", name, err)
		}
	}
}

func TestLinksAreNeverFollowed(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/x.md", "x\n")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "shadow"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A link the member made, in the middle of a target path and as the source.
	if err := os.Symlink(outside, filepath.Join(f.volume, "spaces/private/alice/系统")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "shadow"), filepath.Join(f.volume, "assistant/alice/files/待确认/link.md")); err != nil {
		t.Fatal(err)
	}
	for name, submission := range map[string]string{
		"target through a link": `{"operations":[{"op":"write","path":"/nas/个人空间/系统/shadow","source":"/nas/助手/待确认/x.md"}]}`,
		"source is a link":      `{"operations":[{"op":"write","path":"/nas/个人空间/y.md","source":"/nas/助手/待确认/link.md"}]}`,
	} {
		card, err := f.prepare(submission)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: prepare = %v %v, want ErrInvalid", name, card, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s: the refusal shows what is behind the link: %v", name, err)
		}
	}
	// A folder replaced by a link after the card is not written through either.
	f.put("spaces/private/alice/笔记/a.md", "v1\n")
	submission := `{"operations":[{"op":"write","path":"/nas/个人空间/笔记/a.md","source":"/nas/助手/待确认/x.md"}]}`
	f.approve(submission)
	if err := os.Rename(filepath.Join(f.volume, "spaces/private/alice/笔记"), filepath.Join(f.volume, "spaces/private/alice/笔记-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.volume, "spaces/shared"), filepath.Join(f.volume, "spaces/private/alice/笔记")); err != nil {
		t.Fatal(err)
	}
	result := f.exec.Apply(context.Background(), "alice", f.member, []byte(submission))
	if !strings.Contains(result, "什么都没有改变") {
		t.Fatalf("apply through a swapped link = %s", result)
	}
	if _, err := os.Stat(filepath.Join(f.volume, "spaces/shared/a.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the change was written through the link into the shared space")
	}
}

func TestSeveralOperationsRunInOrderAndKeepEveryOldVersion(t *testing.T) {
	f := newFixture(t)
	f.put("spaces/private/alice/旧.md", "旧\n")
	f.put("spaces/private/alice/.a-nas-trash/alice/计划.md", "更早的\n")
	f.put("assistant/alice/files/待确认/计划.md", "新计划\n")
	f.put("assistant/alice/files/待确认/公告.md", "周日大扫除\n")
	submission := `{"summary":"整理","operations":[
		{"op":"mkdir","path":"/nas/个人空间/归档"},
		{"op":"move","path":"/nas/个人空间/旧.md","to":"/nas/个人空间/归档/旧.md"},
		{"op":"write","path":"/nas/个人空间/计划.md","source":"/nas/助手/待确认/计划.md"},
		{"op":"write","path":"/nas/共享空间/Agent空间/alice/公告.md","source":"/nas/助手/待确认/公告.md"}]}`
	f.approve(submission)
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(submission)); !strings.Contains(result, "4 项更改已执行") {
		t.Fatalf("apply = %s", result)
	}
	checks := map[string]string{
		"spaces/private/alice/归档/旧.md":                             "旧\n",
		"spaces/private/alice/计划.md":                               "新计划\n",
		"spaces/private/alice/.a-nas-trash/alice/计划.md":            "更早的\n",
		"spaces/private/alice/.a-nas-trash/alice/Copy #1 of 计划.md": "# 计划\n- 买菜\n",
		"spaces/shared/Agent空间/alice/公告.md":                        "周日大扫除\n",
	}
	for rel, want := range checks {
		if got := f.get(rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	// Deleting from Agent空间 keeps the copy in the shared recycle bin, in its folders.
	remove := `{"summary":"撤下","operations":[{"op":"delete","path":"/nas/共享空间/Agent空间/alice/公告.md"}]}`
	f.approve(remove)
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(remove)); !strings.Contains(result, "已执行") {
		t.Fatalf("delete = %s", result)
	}
	if got := f.get("spaces/shared/.a-nas-trash/alice/Agent空间/alice/公告.md"); got != "周日大扫除\n" {
		t.Fatalf("deleted copy = %q", got)
	}
	if recent := f.exec.Recent("alice"); !strings.Contains(recent, "已执行：撤下") {
		t.Fatalf("recent = %s", recent)
	}
}

func TestDigestsIgnoreWhatTheToolDoesNotUse(t *testing.T) {
	a, _ := ParseSubmission([]byte(`{"summary":"s","operations":[{"op":"mkdir","path":"/nas/个人空间/x","extra":1}],"note":"n"}`))
	b, _ := ParseSubmission([]byte(`{"operations":[{"path":"/nas/个人空间/x","op":"mkdir"}],"summary":"s"}`))
	if a.Digest() != b.Digest() {
		t.Fatal("the same call has two digests")
	}
	c, _ := ParseSubmission([]byte(`{"summary":"s","operations":[{"op":"mkdir","path":"/nas/个人空间/y"}]}`))
	if a.Digest() == c.Digest() {
		t.Fatal("different calls share a digest")
	}
}
