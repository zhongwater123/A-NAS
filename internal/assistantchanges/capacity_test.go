package assistantchanges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

func TestPrepareBudgetsSnapshotsAndTheFinalWrite(t *testing.T) {
	for _, test := range []struct {
		name     string
		old      *string
		draft    string
		budget   int64
		want     int64
		admitted bool
	}{
		{"new file", nil, "1234", 9, 9, true},
		{"new file without room for both copies", nil, "1234", 8, 9, false},
		{"old preview", new(strings.Repeat("x", 64<<10)), "a", 65539, 65539, true},
		{"old preview without room", new(strings.Repeat("x", 64<<10)), "a", 65538, 65539, false},
		{"empty draft still saves preview", new(strings.Repeat("x", 64<<10)), "", 0, 65537, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.exec.Root = filepath.Join(f.volume, "assistant", ".changes")
			if test.old != nil {
				f.put("spaces/private/alice/target", *test.old)
			}
			f.put("assistant/alice/files/待确认/new", test.draft)
			var checked []int64
			f.exec.Admits = func(path string, incoming int64) (bool, error) {
				if path != f.volume {
					t.Fatalf("admission path = %q", path)
				}
				checked = append(checked, incoming)
				return incoming <= test.budget, nil
			}
			const plan = `{"summary":"replace target","operations":[{"op":"write","path":"/nas/个人空间/target","source":"/nas/助手/待确认/new"}]}`
			card, err := f.prepare(plan)
			if len(checked) != 1 || checked[0] != test.want {
				t.Fatalf("checked = %v, want [%d]", checked, test.want)
			}
			if !test.admitted {
				if !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), "保留容量") {
					t.Fatalf("prepare = %v", err)
				}
				entries, err := os.ReadDir(filepath.Join(f.exec.Root, "alice"))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.IsDir() {
						t.Fatalf("refused change saved snapshots in %s", entry.Name())
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			kept := filepath.Join(f.exec.Root, "alice", card["id"].(string), "0")
			if data, err := os.ReadFile(kept); err != nil || string(data) != test.draft {
				t.Fatalf("snapshot = %q, %v", data, err)
			}
		})
	}
}

func TestApplyMeasuresDirectoryCopiesAgainBeforeAnyStep(t *testing.T) {
	for _, budget := range []int64{4, 8} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			f := newFixture(t)
			f.put("spaces/private/alice/tree/child", "a")
			var checked []int64
			f.exec.Admits = func(_ string, incoming int64) (bool, error) {
				checked = append(checked, incoming)
				return incoming <= budget, nil
			}
			const plan = `{"summary":"copy tree","operations":[{"op":"mkdir","path":"/nas/个人空间/new-folder"},{"op":"copy","path":"/nas/个人空间/copied","source":"/nas/个人空间/tree"}]}`
			f.approve(plan)
			f.edit("spaces/private/alice/tree/child", "12345678")
			result := f.exec.Apply(context.Background(), "alice", f.member, []byte(plan))
			if len(checked) < 2 || checked[0] != 1 || checked[1] != 8 {
				t.Fatalf("checked = %v", checked)
			}
			if budget == 8 {
				if !strings.Contains(result, "2 项更改已执行") || f.get("spaces/private/alice/copied/child") != "12345678" {
					t.Fatalf("apply = %s", result)
				}
				return
			}
			if !strings.Contains(result, "保留容量") || !strings.Contains(result, "什么都没有改变") {
				t.Fatalf("apply = %s", result)
			}
			for _, rel := range []string{"spaces/private/alice/new-folder", "spaces/private/alice/copied"} {
				if _, err := os.Stat(filepath.Join(f.volume, rel)); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("%s changed: %v", rel, err)
				}
			}
		})
	}
}

func TestCopiesWhoseSourceTheBatchChangesAreRefused(t *testing.T) {
	for _, test := range []struct{ name, first, source, target string }{
		{"write source", `{"op":"write","path":"/nas/个人空间/tree/child","source":"/nas/助手/待确认/new"}`, "/nas/个人空间/tree/child", "/nas/个人空间/copied"},
		{"write descendant", `{"op":"write","path":"/nas/个人空间/tree/child","source":"/nas/助手/待确认/new"}`, "/nas/个人空间/tree", "/nas/个人空间/copied"},
		{"copy descendant", `{"op":"copy","path":"/nas/个人空间/tree/added","source":"/nas/个人空间/other"}`, "/nas/个人空间/tree", "/nas/个人空间/copied"},
		{"move into source", `{"op":"move","path":"/nas/个人空间/other","to":"/nas/个人空间/tree/moved"}`, "/nas/个人空间/tree", "/nas/个人空间/copied"},
		{"delete descendant", `{"op":"delete","path":"/nas/个人空间/tree/child"}`, "/nas/个人空间/tree", "/nas/个人空间/copied"},
		{"old file enters source recycle bin", `{"op":"delete","path":"/nas/个人空间/other"}`, "/nas/个人空间/.a-nas-trash/alice", "/nas/个人空间/copied"},
		{"copy into own source", `{"op":"mkdir","path":"/nas/个人空间/empty"}`, "/nas/个人空间/tree", "/nas/个人空间/tree/copied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.put("spaces/private/alice/tree/child", "a")
			f.put("spaces/private/alice/other", "b")
			f.put("assistant/alice/files/待确认/new", "1234")
			plan := fmt.Sprintf(`{"summary":"dependent copy","operations":[%s,{"op":"copy","path":%q,"source":%q}]}`, test.first, test.target, test.source)
			if _, err := f.prepare(plan); !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), "拆成两批") {
				t.Fatalf("prepare = %v (%s)", err, Reasons(err))
			}
			if got := f.get("spaces/private/alice/tree/child"); got != "a" {
				t.Fatalf("source changed: %q", got)
			}
		})
	}
}

func TestCopiesAreAdmittedAsABatchWithoutSubtractingReplacedFiles(t *testing.T) {
	f := newFixture(t)
	f.put("spaces/private/alice/one", "123")
	f.put("spaces/private/alice/tree/two", "1234")
	f.put("spaces/private/alice/first", "an old file larger than both copies")
	const plan = `{"summary":"two copies","operations":[{"op":"copy","path":"/nas/个人空间/first","source":"/nas/个人空间/one"},{"op":"copy","path":"/nas/个人空间/second","source":"/nas/个人空间/tree"}]}`
	f.exec.Admits = func(_ string, incoming int64) (bool, error) { return incoming <= 6, nil }
	if _, err := f.prepare(plan); !errors.Is(err, ErrInvalid) || !strings.Contains(Reasons(err), "保留容量") {
		t.Fatalf("prepare with room for each copy but not both = %v", err)
	}
	f.exec.Admits = func(_ string, incoming int64) (bool, error) { return incoming <= 7, nil }
	f.approve(plan)
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(plan)); !strings.Contains(result, "2 项更改已执行") {
		t.Fatal(result)
	}
	if got := f.get("spaces/private/alice/.a-nas-trash/alice/first"); got != "an old file larger than both copies" {
		t.Fatalf("old file = %q", got)
	}
}

// Growing the source during an earlier, unrelated operation stands for
// another writer using the volume while this batch is running.
type growBeforeCopy struct {
	filesystem.FileSystem
	onMkdir func(string)
}

func (g growBeforeCopy) MkdirAll(ctx context.Context, path string) error {
	g.onMkdir(path)
	return g.FileSystem.MkdirAll(ctx, path)
}

func TestACopyChecksSpaceAgainAfterEarlierSteps(t *testing.T) {
	f := newFixture(t)
	f.put("spaces/private/alice/tree/child", "a")
	f.exec.Admits = func(_ string, incoming int64) (bool, error) { return incoming <= 4, nil }
	const plan = `{"summary":"copy tree","operations":[{"op":"mkdir","path":"/nas/个人空间/new-folder"},{"op":"copy","path":"/nas/个人空间/copied","source":"/nas/个人空间/tree"}]}`
	f.approve(plan)
	f.member = growBeforeCopy{FileSystem: f.member, onMkdir: func(path string) {
		if path == filepath.Join(f.volume, "spaces/private/alice/new-folder") {
			f.edit("spaces/private/alice/tree/child", "12345678")
		}
	}}
	result := f.exec.Apply(context.Background(), "alice", f.member, []byte(plan))
	if !strings.Contains(result, "保留容量") || !strings.Contains(result, "前 1 项已经生效") {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(f.volume, "spaces/private/alice/copied")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("copied directory was made: %v", err)
	}
}

func TestTheOldPreviewStaysWithinItsAdmissionWhenTheFileGrows(t *testing.T) {
	f := newFixture(t)
	f.exec.Root = filepath.Join(f.volume, "assistant", ".changes")
	f.put("spaces/private/alice/target", "x")
	f.put("assistant/alice/files/待确认/new", "a")
	f.exec.Admits = func(_ string, incoming int64) (bool, error) { return incoming <= 4, nil }
	f.member = &watched{FileSystem: f.member, onOpen: func(path string) {
		if path == filepath.Join(f.volume, "spaces/private/alice/target") {
			f.edit("spaces/private/alice/target", strings.Repeat("x", 64<<10))
		}
	}}
	const plan = `{"summary":"replace target","operations":[{"op":"write","path":"/nas/个人空间/target","source":"/nas/助手/待确认/new"}]}`
	card, err := f.prepare(plan)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(f.exec.Root, "alice", card["id"].(string), "0.before"))
	if err != nil || info.Size() != 1 {
		t.Fatalf("old preview = %v, %v", info, err)
	}
}
