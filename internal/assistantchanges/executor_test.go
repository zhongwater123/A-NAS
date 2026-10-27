package assistantchanges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

type fixture struct {
	t      *testing.T
	volume string
	member filesystem.FileSystem
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
	f := &fixture{t: t, volume: volume, member: filesystem.NewRootFileSystem(volume, false), clock: time.Unix(1_800_000_000, 0)}
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

// edit writes rel as someone editing it later would: a minute ahead, so the
// change shows whatever the file system's time resolution, without a sleep.
func (f *fixture) edit(rel, content string) {
	f.t.Helper()
	f.put(rel, content)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(f.volume, rel), later, later); err != nil {
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
	f.edit("spaces/private/alice/计划.md", "成员自己改的\n")
	result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan))
	if !strings.Contains(result, "被改动过") || !strings.Contains(result, "什么都没有改变") {
		t.Fatalf("apply = %s", result)
	}
	if got := f.get("spaces/private/alice/计划.md"); got != "成员自己改的\n" {
		t.Fatalf("plan = %q", got)
	}
}

func TestADamagedCopyIsNotWritten(t *testing.T) {
	// The copy is checked against its SHA-256 while it is written.
	for name, damage := range map[string]func(string) error{
		"cut short":   func(name string) error { return os.Truncate(name, 3) },
		"same length": func(name string) error { return os.WriteFile(name, []byte("别人的版本\n"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.put("assistant/alice/files/待确认/计划.md", "助手的版本\n")
			id := f.approve(editPlan)
			if err := damage(filepath.Join(f.exec.Root, "alice", id, "0")); err != nil {
				t.Fatal(err)
			}
			result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan))
			if !strings.Contains(result, "新内容已不完整") || !strings.Contains(result, "什么都没有改变") {
				t.Fatalf("apply = %s", result)
			}
			if got := f.get("spaces/private/alice/计划.md"); got != "# 计划\n- 买菜\n" {
				t.Fatalf("plan = %q", got)
			}
			if left, _ := filepath.Glob(filepath.Join(f.volume, "spaces/private/alice/.a-nas-assistant-*")); len(left) > 0 {
				t.Fatalf("left behind: %v", left)
			}
		})
	}
}

func TestALargeDraftIsCopiedWholeAndOnlyItsStartOutlivesTheChange(t *testing.T) {
	f := newFixture(t)
	draft := strings.Repeat("一行字\n", (1<<20)/10)
	f.put("assistant/alice/files/待确认/计划.md", draft)
	card, err := f.prepare(editPlan)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := card["operations"].([]any)[0].(map[string]any)["after"].(string)
	if len(after) < previewLimit-3 || len(after) > previewLimit || !strings.HasPrefix(draft, after) {
		t.Fatalf("the card shows %d bytes of the draft", len(after))
	}
	if _, err := f.exec.Decide("alice", card["id"].(string), "approve"); err != nil {
		t.Fatal(err)
	}
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan)); !strings.Contains(result, "1 项更改已执行") {
		t.Fatalf("apply = %s", result)
	}
	if got := f.get("spaces/private/alice/计划.md"); got != draft {
		t.Fatalf("the plan has %d bytes, want %d", len(got), len(draft))
	}
	// A rejected change, like an applied one, keeps only what its card shows.
	f.put("assistant/alice/files/待确认/计划.md", draft+"再加一行\n")
	rejected, err := f.prepare(editPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec.Decide("alice", rejected["id"].(string), "reject"); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(f.exec.Root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err == nil && info.Size() > previewLimit {
			t.Errorf("%s keeps %d bytes", name, info.Size())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	applied, _ := f.exec.Get("alice", card["id"].(string))
	var shown struct{ Operations []struct{ After string } }
	if err := json.Unmarshal(applied, &shown); err != nil || shown.Operations[0].After != after {
		t.Fatalf("the applied card no longer shows the start: %v", err)
	}
}

func TestCopiesAreHeldOnlyWhileAChangeCanBeApplied(t *testing.T) {
	defer func(limit int64) { maxPending = limit }(maxPending)
	maxPending = 3 << 20
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", strings.Repeat("x", 1<<20))
	prepare := func(session string) (string, error) {
		t.Helper()
		raw, err := f.exec.Prepare(context.Background(), "alice", f.member, []byte(editPlan), session, "req-1")
		if err != nil {
			return "", err
		}
		var card struct{ ID string }
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatal(err)
		}
		return card.ID, nil
	}
	must := func(session string) string {
		t.Helper()
		id, err := prepare(session)
		if err != nil {
			t.Fatalf("prepare in %s: %v", session, err)
		}
		return id
	}
	held := func(id string) int64 {
		t.Helper()
		info, err := os.Stat(filepath.Join(f.exec.Root, "alice", id, "0"))
		if err != nil {
			return 0
		}
		return info.Size()
	}
	status := func(id string) string {
		t.Helper()
		raw, err := f.exec.Get("alice", id)
		if err != nil {
			t.Fatal(err)
		}
		var card struct{ Status string }
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatal(err)
		}
		return card.Status
	}
	superseded, rejected, expired := must("s1"), must("s2"), must("s3")
	if _, err := prepare("s4"); !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), "等待确认") {
		t.Fatalf("a fourth copy while three wait: %v", err)
	}
	// A session's next submission replaces the card it waited for.
	replacing := must("s1")
	if status(superseded) != StatusExpired {
		t.Fatalf("the replaced card is %s", status(superseded))
	}
	if _, err := f.exec.Decide("alice", rejected, "reject"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec.Decide("alice", expired, "expire"); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"replaced": superseded, "rejected": rejected, "expired": expired} {
		if size := held(id); size > previewLimit {
			t.Errorf("the %s change still holds %d bytes", name, size)
		}
	}
	if held(replacing) != 1<<20 {
		t.Fatalf("the waiting change holds %d bytes", held(replacing))
	}
	// A card nobody answered for longer than Hermes waits is void.
	waiting := must("s4")
	f.clock = f.clock.Add(awaitTTL + time.Second)
	must("s5")
	if status(waiting) != StatusExpired || held(waiting) > previewLimit {
		t.Fatalf("a day-old card is %s and holds %d bytes", status(waiting), held(waiting))
	}
	// Only the newest changes that ended keep their texts at all.
	for range keepTexts {
		id := must("s6")
		if _, err := f.exec.Decide("alice", id, "reject"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.exec.Root, "alice", superseded)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an old change keeps its texts: %v", err)
	}
}

func TestEachRefusalIsRepeatedForItsOwnSubmission(t *testing.T) {
	f := newFixture(t)
	member := &watched{FileSystem: f.member}
	f.member = member
	missing := `{"summary":"a","operations":[{"op":"write","path":"/nas/个人空间/a.md","source":"/nas/助手/待确认/a.md"}]}`
	absent := `{"summary":"b","operations":[{"op":"delete","path":"/nas/个人空间/没有.md"}]}`
	for _, submission := range []string{missing, absent} {
		if _, err := f.prepare(submission); !errors.Is(err, ErrInvalid) {
			t.Fatalf("prepare = %v, want ErrInvalid", err)
		}
	}
	// Another conversation's card in between leaves both reasons in place.
	if _, err := f.exec.Prepare(context.Background(), "alice", member, []byte(`{"summary":"c","operations":[{"op":"mkdir","path":"/nas/个人空间/c"}]}`), "other", "req-2"); err != nil {
		t.Fatal(err)
	}
	for submission, want := range map[string]string{missing: "已写好的文件", absent: "没有.md 不存在"} {
		member.forget()
		if result := f.exec.Apply(context.Background(), "alice", member, []byte(submission)); !strings.Contains(result, want) {
			t.Errorf("apply = %s, want %q", result, want)
		}
		if len(member.looked)+len(member.opened) > 0 {
			t.Errorf("apply looked at %v and opened %v", member.looked, member.opened)
		}
	}
}

func TestAChangeLeftApplyingByAnEarlierProcessHoldsNothing(t *testing.T) {
	defer func(limit int64) { maxPending = limit }(maxPending)
	maxPending = 3 << 19
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", strings.Repeat("x", 1<<20))
	id := f.approve(editPlan)
	// The Host Agent stops while applying it.
	changes, err := f.exec.load("alice")
	if err != nil {
		t.Fatal(err)
	}
	changes[0].Status = StatusApplying
	data, _ := json.Marshal(changes)
	if err := os.WriteFile(filepath.Join(f.exec.Root, "alice", "changes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := &Executor{Root: f.exec.Root, Volume: f.volume, Now: f.exec.Now}
	if _, err := restarted.Prepare(context.Background(), "alice", f.member, []byte(editPlan), "next", "req-2"); err != nil {
		t.Fatalf("a change nobody applies any more still holds its copy: %v", err)
	}
	if info, err := os.Stat(filepath.Join(f.exec.Root, "alice", id, "0")); err != nil || info.Size() > previewLimit {
		t.Fatalf("the stuck change's copy: %v %v", info, err)
	}
}

func TestACardOlderThanHermesWaitsCannotBeApproved(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", "新\n")
	card, err := f.prepare(editPlan)
	if err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(awaitTTL + time.Second)
	if _, err := f.exec.Decide("alice", card["id"].(string), "approve"); !errors.Is(err, ErrDecided) {
		t.Fatalf("approving a day-old card: %v", err)
	}
}

func TestCopiesOfChangesThatEndedAreCutBeforeNewOnesAreMade(t *testing.T) {
	f := newFixture(t)
	member := &watched{FileSystem: f.member}
	f.member = member
	f.put("assistant/alice/files/待确认/计划.md", strings.Repeat("x", 1<<20))
	id := f.approve(editPlan)
	// Hermes never applies it, and the grant runs out.
	f.clock = f.clock.Add(GrantTTL + time.Second)
	held := int64(-1)
	member.onOpen = func(path string) {
		if strings.HasSuffix(path, "待确认/计划.md") {
			if info, err := os.Stat(filepath.Join(f.exec.Root, "alice", id, "0")); err == nil {
				held = info.Size()
			}
		}
	}
	if _, err := f.exec.Prepare(context.Background(), "alice", member, []byte(editPlan), "next", "req-2"); err != nil {
		t.Fatal(err)
	}
	if held < 0 || held > previewLimit {
		t.Fatalf("the lapsed change held %d bytes while the next was copied", held)
	}
}

func TestADecidedCardKeepsItsTextsForAWhile(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", "新计划\n")
	raw, err := f.exec.Prepare(context.Background(), "alice", f.member, []byte(editPlan), "first", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	var first struct{ ID string }
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	for i := range keepTexts {
		if _, err := f.exec.Prepare(context.Background(), "alice", f.member, []byte(editPlan), fmt.Sprint("later-", i), "req-2"); err != nil {
			t.Fatal(err)
		}
	}
	card, err := f.exec.Decide("alice", first.ID, "reject")
	if err != nil {
		t.Fatal(err)
	}
	var shown struct {
		Operations []struct{ Before, After string }
	}
	if err := json.Unmarshal(card, &shown); err != nil || shown.Operations[0].After != "新计划\n" || shown.Operations[0].Before != "# 计划\n- 买菜\n" {
		t.Fatalf("the rejected card lost its texts: %s", card)
	}
	f.clock = f.clock.Add(GrantTTL + time.Second)
	if _, err := f.exec.Prepare(context.Background(), "alice", f.member, []byte(editPlan), "much-later", "req-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.exec.Root, "alice", first.ID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an old ended change keeps its texts: %v", err)
	}
}

func TestTidyingUpThatFailsDoesNotStopTheAccount(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes the folder anyway")
	}
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", "新\n")
	first := f.approve(editPlan)
	if _, err := f.exec.Decide("alice", first, "expire"); err != nil {
		t.Fatal(err)
	}
	stuck := filepath.Join(f.exec.Root, "alice", first)
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	f.clock = f.clock.Add(GrantTTL + time.Second)
	for i := range keepTexts + 1 {
		if _, err := f.exec.Prepare(context.Background(), "alice", f.member, []byte(editPlan), fmt.Sprint("s", i), "req-2"); err != nil {
			t.Fatalf("prepare %d with a folder that cannot be removed: %v", i, err)
		}
	}
	if err := os.Chmod(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec.Prepare(context.Background(), "alice", f.member, []byte(editPlan), "last", "req-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stuck); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the folder is not removed once it can be: %v", err)
	}
}

func TestTheCapIsCheckedBeforeFoldersAreMeasured(t *testing.T) {
	defer func(limit int64) { maxPending = limit }(maxPending)
	maxPending = 3 << 19
	f := newFixture(t)
	member := &watched{FileSystem: f.member}
	f.member = member
	f.put("assistant/alice/files/待确认/计划.md", strings.Repeat("x", 1<<20))
	f.put("spaces/shared/旅行/1.jpg", "one")
	if _, err := f.exec.Prepare(context.Background(), "alice", member, []byte(editPlan), "first", "req-1"); err != nil {
		t.Fatal(err)
	}
	member.forget()
	both := `{"summary":"x","operations":[` +
		`{"op":"copy","path":"/nas/个人空间/旅行","source":"/nas/共享空间/旅行"},` +
		`{"op":"write","path":"/nas/个人空间/计划.md","source":"/nas/助手/待确认/计划.md"}]}`
	if _, err := f.exec.Prepare(context.Background(), "alice", member, []byte(both), "second", "req-2"); !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), "等待确认") {
		t.Fatalf("prepare over the cap: %v", err)
	}
	if len(member.measured)+len(member.opened) > 0 {
		t.Fatalf("measured %v and opened %v before refusing", member.measured, member.opened)
	}
}

func TestAccountsMakeChangesSideBySide(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	for _, account := range []string{"alice", "bob"} {
		for i := range 4 {
			wg.Go(func() {
				submission := []byte(fmt.Sprintf(`{"summary":"%d","operations":[{"op":"mkdir","path":"/nas/个人空间/%d"}]}`, i, i))
				// Each in a conversation of its own: a session waits for one card at a time.
				raw, err := f.exec.Prepare(context.Background(), account, f.member, submission, fmt.Sprintf("%s-%d", account, i), "req-1")
				if err != nil {
					t.Errorf("%s %d: prepare: %v", account, i, err)
					return
				}
				var card struct{ ID string }
				if err := json.Unmarshal(raw, &card); err != nil {
					t.Errorf("%s %d: card: %v", account, i, err)
					return
				}
				if _, err := f.exec.Decide(account, card.ID, "approve"); err != nil {
					t.Errorf("%s %d: approve: %v", account, i, err)
					return
				}
				if result := f.exec.Apply(context.Background(), account, f.member, submission); !strings.Contains(result, "1 项更改已执行") {
					t.Errorf("%s %d: apply = %s", account, i, result)
				}
			})
		}
	}
	wg.Wait()
	for _, account := range []string{"alice", "bob"} {
		if recent := f.exec.Recent(account); strings.Count(recent, "已执行") != 4 {
			t.Errorf("%s: recent = %s", account, recent)
		}
	}
}

// watched is the member's file system, noting every path looked at or opened.
type watched struct {
	filesystem.FileSystem
	looked, opened, measured []string
	// onOpen, when set, runs before a file is opened.
	onOpen func(path string)
}

func (w *watched) Lstat(ctx context.Context, path string) (filesystem.FileInfo, error) {
	w.looked = append(w.looked, path)
	return w.FileSystem.Lstat(ctx, path)
}

func (w *watched) Open(ctx context.Context, path string) (*os.File, error) {
	w.opened = append(w.opened, path)
	if w.onOpen != nil {
		w.onOpen(path)
	}
	return w.FileSystem.Open(ctx, path)
}

func (w *watched) Usage(ctx context.Context, path string) (int64, error) {
	w.measured = append(w.measured, path)
	return w.FileSystem.Usage(ctx, path)
}

func (w *watched) forget() { w.looked, w.opened, w.measured = nil, nil, nil }

func TestASubmissionWithoutAGrantOpensNoFile(t *testing.T) {
	f := newFixture(t)
	member := &watched{FileSystem: f.member}
	f.member = member
	f.put("assistant/alice/files/待确认/计划.md", "新内容\n")
	apply := func(submission, want string) {
		t.Helper()
		member.forget()
		if result := f.exec.Apply(context.Background(), "alice", member, []byte(submission)); !strings.Contains(result, want) {
			t.Fatalf("apply = %s, want %q", result, want)
		}
		if len(member.looked)+len(member.opened) > 0 {
			t.Fatalf("apply without a grant looked at %v and opened %v", member.looked, member.opened)
		}
	}
	apply(editPlan, "没有得到用户本人的确认")
	apply(`{"operations":[{"op":"write","path":"/nas/共享空间/通知.md","source":"/nas/助手/待确认/计划.md"}]}`, "只能更改")
	// What Prepare refused is repeated, not looked at again.
	missing := `{"summary":"x","operations":[{"op":"write","path":"/nas/个人空间/y.md","source":"/nas/助手/待确认/y.md"}]}`
	if _, err := f.prepare(missing); !errors.Is(err, ErrInvalid) {
		t.Fatalf("prepare = %v, want ErrInvalid", err)
	}
	apply(missing, "已写好的文件")
	// The same file system applies what the member approved.
	f.approve(editPlan)
	member.forget()
	if result := f.exec.Apply(context.Background(), "alice", member, []byte(editPlan)); !strings.Contains(result, "1 项更改已执行") {
		t.Fatalf("apply = %s", result)
	}
	if got := f.get("spaces/private/alice/计划.md"); got != "新内容\n" {
		t.Fatalf("plan = %q", got)
	}
}

func TestOversizedSubmissionsAreRefusedBeforeAnyFileIsOpened(t *testing.T) {
	f := newFixture(t)
	member := &watched{FileSystem: f.member}
	f.member = member
	many := make([]string, MaxOperations+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"op":"mkdir","path":"/nas/个人空间/%d"}`, i)
	}
	for name, c := range map[string]struct{ submission, want string }{
		"too many operations": {`{"summary":"x","operations":[` + strings.Join(many, ",") + `]}`, "最多 100 项"},
		"too long":            {`{"summary":"` + strings.Repeat("长", MaxSubmission/3+1) + `","operations":[{"op":"mkdir","path":"/nas/个人空间/x"}]}`, "64 KiB"},
		"arguments too large": {`{"summary":"x","operations":[{"op":"mkdir","path":"/nas/个人空间/x"}],"note":"` + strings.Repeat("x", maxArguments) + `"}`, "参数超过 1024 KiB"},
	} {
		member.forget()
		if _, err := f.prepare(c.submission); !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), c.want) {
			t.Errorf("%s: prepare = %v, want %q", name, err, c.want)
		}
		if result := f.exec.Apply(context.Background(), "alice", member, []byte(c.submission)); !strings.Contains(result, c.want) {
			t.Errorf("%s: apply = %s, want %q", name, result, c.want)
		}
		if len(member.looked)+len(member.opened) > 0 {
			t.Errorf("%s: looked at %v and opened %v", name, member.looked, member.opened)
		}
	}
	// Staged files are measured by their metadata; none is opened when they are too large.
	sparse := func(rel string, size int64) string {
		t.Helper()
		f.put("assistant/alice/files/待确认/"+rel, "")
		if err := os.Truncate(filepath.Join(f.volume, "assistant/alice/files/待确认", rel), size); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf(`{"op":"write","path":"/nas/个人空间/%s","source":"/nas/助手/待确认/%s"}`, rel, rel)
	}
	var together []string
	for i := range MaxStaged/MaxContent + 1 {
		together = append(together, sparse(fmt.Sprintf("%d.bin", i), MaxContent))
	}
	for name, c := range map[string]struct{ submission, want string }{
		"a file too large":         {`{"operations":[` + sparse("大.bin", MaxContent+1) + `]}`, "超过 64 MiB"},
		"files together too large": {`{"operations":[` + strings.Join(together, ",") + `]}`, "合计超过 256 MiB"},
	} {
		member.forget()
		if _, err := f.prepare(c.submission); !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), c.want) {
			t.Errorf("%s: prepare = %v, want %q", name, err, c.want)
		}
		if len(member.opened) > 0 {
			t.Errorf("%s: opened %v", name, member.opened)
		}
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
		"move between spaces":  `{"operations":[{"op":"move","path":"/nas/个人空间/计划.md","to":"/nas/共享空间/Agent空间/alice/计划.md"}]}`,
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
		if strings.Contains(Reasons(err), "secret") {
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

func TestACopyReachesThePersonalSpaceFromWhereTheAssistantReads(t *testing.T) {
	f := newFixture(t)
	png := "\x89PNG\r\n\x1a\n\x00\x00binary"
	f.put("spaces/shared/头像.png", png)
	f.put("spaces/private/alice/头像.png", "旧头像")
	copyAvatar := `{"summary":"复制头像","operations":[{"op":"copy","path":"/nas/个人空间/头像.png","source":"/nas/共享空间/头像.png"}]}`
	card, err := f.prepare(copyAvatar)
	if err != nil {
		t.Fatal(err)
	}
	op := card["operations"].([]any)[0].(map[string]any)
	if op["op"] != "copy" || op["from"] != "/nas/共享空间/头像.png" || op["size"] != float64(len(png)) || op["exists"] != true {
		t.Fatalf("card = %v", card)
	}
	if _, err := f.exec.Decide("alice", card["id"].(string), "approve"); err != nil {
		t.Fatal(err)
	}
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(copyAvatar)); !strings.Contains(result, "1 项更改已执行") {
		t.Fatalf("apply = %s", result)
	}
	if got := f.get("spaces/private/alice/头像.png"); got != png {
		t.Fatalf("copy = %q", got)
	}
	if got := f.get("spaces/shared/头像.png"); got != png {
		t.Fatalf("the source changed: %q", got)
	}
	if got := f.get("spaces/private/alice/.a-nas-trash/alice/头像.png"); got != "旧头像" {
		t.Fatalf("replaced file in the recycle bin = %q", got)
	}
}

func TestAFolderCopiesWholeAndNeverOverAnotherFolder(t *testing.T) {
	f := newFixture(t)
	f.put("spaces/shared/旅行/1.jpg", "one")
	f.put("spaces/shared/旅行/行程/2.txt", "two!")
	copyTrip := `{"summary":"复制旅行","operations":[{"op":"copy","path":"/nas/个人空间/旅行","source":"/nas/共享空间/旅行"}]}`
	card, err := f.prepare(copyTrip)
	if err != nil {
		t.Fatal(err)
	}
	op := card["operations"].([]any)[0].(map[string]any)
	if op["folder"] != true || op["size"] != float64(7) {
		t.Fatalf("card = %v", card)
	}
	if _, err := f.exec.Decide("alice", card["id"].(string), "approve"); err != nil {
		t.Fatal(err)
	}
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(copyTrip)); !strings.Contains(result, "已执行") {
		t.Fatalf("apply = %s", result)
	}
	if f.get("spaces/private/alice/旅行/行程/2.txt") != "two!" || f.get("spaces/private/alice/旅行/1.jpg") != "one" {
		t.Fatal("the folder was not copied whole")
	}
	if _, err := f.prepare(copyTrip); !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), "不是普通文件") {
		t.Fatalf("a folder over a folder: %v", err)
	}
}

func TestACopyWhoseSourceChangedAfterTheCardIsNotMade(t *testing.T) {
	f := newFixture(t)
	f.put("spaces/shared/通知.md", "原文\n")
	copyNote := `{"summary":"复制通知","operations":[{"op":"copy","path":"/nas/个人空间/通知.md","source":"/nas/共享空间/通知.md"}]}`
	f.approve(copyNote)
	f.edit("spaces/shared/通知.md", "别人改过了\n")
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(copyNote)); !strings.Contains(result, "被改动过") {
		t.Fatalf("apply = %s", result)
	}
	if got := f.get("spaces/private/alice/通知.md"); !strings.HasPrefix(got, "<") {
		t.Fatalf("copied anyway: %q", got)
	}
}

func TestCopiesComeOnlyFromWhatTheAssistantReads(t *testing.T) {
	f := newFixture(t)
	f.put("spaces/private/bob/私密.md", "bob\n")
	for source, want := range map[string]string{
		"/etc/passwd": "只能复制",
		"/nas/其他/x":   "只能复制",
		"/nas/共享空间/../../private/bob/私密.md": "..",
		"/nas/共享空间/不存在.md":                  "不存在",
	} {
		submission := `{"summary":"x","operations":[{"op":"copy","path":"/nas/个人空间/x.md","source":"` + source + `"}]}`
		if _, err := f.prepare(submission); !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), want) {
			t.Errorf("copy from %s: %v, want %q", source, err, want)
		}
	}
	if _, err := f.prepare(`{"summary":"x","operations":[{"op":"copy","path":"/nas/共享空间/x.md","source":"/nas/个人空间/计划.md"}]}`); err == nil {
		t.Fatal("a copy into Shared was accepted")
	}
}

// Why a submission is refused reaches the assistant in the tool's reply; the
// error value itself stays plain (#222).
func TestRefusalReasonsAreInTheReplyNotInTheError(t *testing.T) {
	t.Parallel()
	_, err := ParseSubmission([]byte("not json"))
	han := func(r rune) bool { return unicode.Is(unicode.Han, r) }
	if !errors.Is(err, ErrInvalid) || strings.ContainsFunc(err.Error(), han) || strings.ContainsFunc(ErrDecided.Error(), han) {
		t.Fatalf("errors = %q, %q; want plain errors that match ErrInvalid", err, ErrDecided)
	}
	if got := Reasons(err); got != "参数不是有效的 JSON 对象" {
		t.Fatalf("Reasons() = %q", got)
	}
	if got := Reasons(errors.New("other")); got != "" {
		t.Fatalf("Reasons() of another error = %q, want none", got)
	}
}
