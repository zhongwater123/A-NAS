package assistantchanges

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestShortBinarySuffixIsNotPresentedAsCompleteText(t *testing.T) {
	for _, draft := range []string{"\xff", "safe\xff", "safe\xe4\xb8", "safe\xff\xfe\xfd"} {
		t.Run(fmt.Sprintf("%x", draft), func(t *testing.T) {
			f := newFixture(t)
			f.put("assistant/alice/files/待确认/计划.md", draft)
			const plan = `{"summary":"publish draft","operations":[{"op":"write","path":"/nas/共享空间/Agent空间/alice/published.bin","source":"/nas/助手/待确认/计划.md"}]}`
			card, err := f.prepare(plan)
			if err != nil {
				t.Fatal(err)
			}
			op := card["operations"].([]any)[0].(map[string]any)
			if op["binary"] != true && op["partial"] != true {
				t.Fatalf("incomplete preview has neither warning flag: original=%x after=%q size=%v binary=%v partial=%v", draft, op["after"], op["size"], op["binary"], op["partial"])
			}
		})
	}
}

func TestReplacingBinaryFileSaysOldContentIsNotShown(t *testing.T) {
	f := newFixture(t)
	f.put("spaces/private/alice/计划.md", "\xff\xd8\xff\xe0"+strings.Repeat("x", previewLimit))
	f.put("assistant/alice/files/待确认/计划.md", "replacement text")
	card, err := f.prepare(editPlan)
	if err != nil {
		t.Fatal(err)
	}
	op := card["operations"].([]any)[0].(map[string]any)
	if op["before"] == nil && op["beforeBinary"] != true {
		t.Fatalf("old binary content is absent without a binary marker: %v", op)
	}
	if op["beforePartial"] == true || op["beforeHiddenSize"] != float64(previewLimit+4) {
		t.Fatalf("binary content was described as a text prefix: %v", op)
	}
}

func TestTheCardCountsTheBytesHiddenAtAUTF8Boundary(t *testing.T) {
	f := newFixture(t)
	draft := strings.Repeat("x", previewLimit-1) + "猫"
	f.put("spaces/private/alice/计划.md", draft)
	f.put("assistant/alice/files/待确认/计划.md", draft)
	id := f.approve(editPlan)
	check := func() {
		t.Helper()
		raw, err := f.exec.Get("alice", id)
		if err != nil {
			t.Fatal(err)
		}
		var card map[string]any
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatal(err)
		}
		op := card["operations"].([]any)[0].(map[string]any)
		if op["partial"] != true || op["beforePartial"] != true || op["binary"] != nil || op["beforeBinary"] != nil {
			t.Fatalf("UTF-8 boundary flags = %v", op)
		}
		if op["hiddenSize"] != float64(len("猫")) || op["beforeHiddenSize"] != float64(len("猫")) {
			t.Fatalf("UTF-8 boundary hidden bytes = %v / %v", op["hiddenSize"], op["beforeHiddenSize"])
		}
		if got := op["after"].(string); len(got) != previewLimit-1 {
			t.Fatalf("shown bytes = %d", len(got))
		}
	}
	check()
	if result := f.exec.Apply(context.Background(), "alice", f.member, []byte(editPlan)); !strings.Contains(result, "1 项更改已执行") {
		t.Fatal(result)
	}
	check()
}

func TestAnInvalidByteAtThePreviewLimitIsNotAnIncompleteRune(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", strings.Repeat("x", previewLimit-1)+"\xffmore bytes")
	card, err := f.prepare(editPlan)
	if err != nil {
		t.Fatal(err)
	}
	op := card["operations"].([]any)[0].(map[string]any)
	if op["binary"] != true || op["after"] != nil {
		t.Fatalf("invalid byte at the preview limit = %v", op)
	}
}

func TestAnEmptyDraftIsShownWhole(t *testing.T) {
	f := newFixture(t)
	f.put("assistant/alice/files/待确认/计划.md", "")
	card, err := f.prepare(editPlan)
	if err != nil {
		t.Fatal(err)
	}
	op := card["operations"].([]any)[0].(map[string]any)
	if op["after"] != "" || op["size"] != float64(0) || op["binary"] != nil || op["partial"] != nil || op["hiddenSize"] != nil {
		t.Fatalf("empty draft = %v", op)
	}
}
