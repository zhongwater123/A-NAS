package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/workspace"
)

type fixture struct {
	store *workspace.Store
	now   *time.Time
	full  *bool
	admin workspace.Actor
	alice workspace.Actor
	bob   workspace.Actor
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	full := false
	store, err := workspace.Open(t.TempDir(), workspace.Options{
		Now:    func() time.Time { return now },
		Admits: func(string, int64) (bool, error) { return !full, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return fixture{
		store: store, now: &now, full: &full,
		admin: workspace.Actor{AccountID: "user:owner", Name: "owner", Admin: true},
		alice: workspace.Actor{AccountID: "user:alice", Name: "Alice"},
		bob:   workspace.Actor{AccountID: "user:bob", Name: "bob"},
	}
}

func claude(tokenID string) workspace.Via {
	return workspace.Via{Kind: workspace.ViaClaudeCode, Name: "办公电脑", TokenID: tokenID}
}

func TestAPageKeepsEveryVersionWithWhoSavedItAndHow(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	source := readSample(t)

	page, err := f.store.CreatePage(ctx, f.alice, `C:\work\prd.html`, source, workspace.Web, "")
	if err != nil {
		t.Fatalf("CreatePage() error = %v", err)
	}
	if page.Title != "家庭相册分享 PRD" || page.CreatedBy != (workspace.Person{ID: "user:alice", Name: "Alice"}) || page.Latest.Version != 1 ||
		page.Latest.Via.Kind != workspace.ViaWeb || page.Latest.Size != int64(len(source)) || len(page.Versions) != 1 {
		t.Fatalf("created page = %+v", page)
	}

	*f.now = f.now.Add(time.Minute)
	updated := bytes.Replace(source, []byte("发出分享"), []byte("发送"), 1)
	saved, err := f.store.SaveVersion(ctx, f.bob, page.ID, 1, updated, claude("agent:bob"), "  改按钮文字 ")
	if err != nil {
		t.Fatalf("SaveVersion() error = %v", err)
	}
	if saved.Version != 2 || saved.SavedBy.Name != "bob" || saved.Via != (workspace.Via{Kind: workspace.ViaClaudeCode, Name: "办公电脑"}) || saved.Note != "改按钮文字" {
		t.Fatalf("saved = %+v", saved)
	}
	content, summary, err := f.store.Content(ctx, page.ID, 0)
	if err != nil || !bytes.Equal(content, updated) || summary.Latest.Version != 2 {
		t.Fatalf("latest content: %v, %+v", err, summary)
	}
	if first, _, err := f.store.Content(ctx, page.ID, 1); err != nil || !bytes.Equal(first, source) {
		t.Fatalf("version 1 is not what was uploaded: %v", err)
	}
	if _, _, err := f.store.Content(ctx, page.ID, 3); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("a version that does not exist: %v", err)
	}

	// A whole page saved over v1 after v2 is refused and names v2.
	_, err = f.store.SaveVersion(ctx, f.alice, page.ID, 1, source, workspace.Web, "")
	var changed *workspace.PageChangedError
	if !errors.As(err, &changed) || changed.Latest.Version != 2 || changed.Latest.SavedBy.Name != "bob" {
		t.Fatalf("saving over an old version: %v", err)
	}

	*f.now = f.now.Add(time.Minute)
	second, err := f.store.CreatePage(ctx, f.bob, "notes.htm", []byte("<p>no title</p>"), workspace.Web, "")
	if err != nil || second.Title != "notes" {
		t.Fatalf("a page without a title: %+v, %v", second, err)
	}
	listed, err := f.store.ListPages(ctx)
	if err != nil || len(listed) != 2 || listed[0].ID != second.ID || listed[1].Latest.Version != 2 {
		t.Fatalf("ListPages() = %+v, %v", listed, err)
	}
	full, err := f.store.Page(ctx, page.ID)
	if err != nil || len(full.Versions) != 2 || full.Versions[0].Version != 2 || full.Versions[1].Version != 1 {
		t.Fatalf("Page() = %+v, %v", full, err)
	}
}

func TestPagesMustBeUTF8HTMLWithinTheLimits(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	for name, content := range map[string][]byte{
		"empty":     {},
		"not UTF-8": {0xff, 0xfe, '<', 'p', '>'},
	} {
		if _, err := f.store.CreatePage(ctx, f.alice, "a.html", content, workspace.Web, ""); !errors.Is(err, workspace.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.store.CreatePage(ctx, f.alice, "a.html", bytes.Repeat([]byte("a"), workspace.MaxPageBytes+1), workspace.Web, ""); !errors.Is(err, workspace.ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if _, err := f.store.CreatePage(ctx, f.alice, "a.html", []byte("<p>"), workspace.Web, strings.Repeat("说", workspace.MaxNoteRunes+1)); !errors.Is(err, workspace.ErrInvalid) {
		t.Errorf("a long note: %v", err)
	}
	*f.full = true
	if _, err := f.store.CreatePage(ctx, f.alice, "a.html", []byte("<p>"), workspace.Web, ""); !errors.Is(err, workspace.ErrVolumeFull) {
		t.Errorf("a full volume: %v", err)
	}
}

func TestAgentEditsLandOnTheLatestVersionAndFailOnlyWhereTheTextIsGone(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	source := readSample(t)
	page, err := f.store.CreatePage(ctx, f.alice, "prd.html", source, workspace.Web, "")
	if err != nil {
		t.Fatal(err)
	}
	button := ".btn { border: 0; border-radius: 10px; padding: 14px 28px; font-size: 16px; font-weight: 600; cursor: pointer; }"

	// Alice's agent and Bob's both read v1. Alice's makes the buttons
	// smaller; Bob's changes the status, elsewhere on the page.
	first, err := f.store.EditPage(ctx, f.alice, page.ID, 1, []workspace.Edit{
		{OldText: button, NewText: strings.Replace(button, "padding: 14px 28px; font-size: 16px", "padding: 8px 18px; font-size: 14px", 1)},
		{OldText: `<button class="btn btn-primary">发出分享</button>`, NewText: `<button class="btn btn-primary">发送</button>`},
	}, claude("agent:alice"), "把按钮改小")
	if err != nil {
		t.Fatalf("first edit: %v", err)
	}
	if first.Version.Version != 2 || first.OnTopOf != 1 || len(first.Lines) != 2 || first.Lines[1] != [2]int{108, 108} || first.Version.Note != "把按钮改小" {
		t.Fatalf("first edit = %+v", first)
	}
	v2, _, _ := f.store.Content(ctx, page.ID, 2)
	buttonAt := bytes.Index(v2, []byte(`<button class="btn btn-primary">`))
	styleAt := bytes.Index(v2, []byte("<style>"))
	if len(first.Version.Changed) != 2 || first.Version.Changed[0] != styleAt || first.Version.Changed[1] != buttonAt {
		t.Fatalf("changed = %v, want the style %d and the button %d", first.Version.Changed, styleAt, buttonAt)
	}

	second, err := f.store.EditPage(ctx, f.bob, page.ID, 1, []workspace.Edit{
		{OldText: `<span class="live">评审中</span>`, NewText: `<span class="live">已通过</span>`},
	}, claude("agent:bob"), "状态改成已通过")
	if err != nil {
		t.Fatalf("an edit elsewhere from an older version: %v", err)
	}
	if second.Version.Version != 3 || second.OnTopOf != 2 {
		t.Fatalf("second edit = %+v", second)
	}
	latest, _, _ := f.store.Content(ctx, page.ID, 0)
	for _, kept := range []string{"padding: 8px 18px", ">发送<", ">已通过<"} {
		if !bytes.Contains(latest, []byte(kept)) {
			t.Fatalf("v3 lost %q", kept)
		}
	}

	// Bob's agent changes the same button rule it read in v1: refused, and
	// nothing of the call is saved.
	_, err = f.store.EditPage(ctx, f.bob, page.ID, 1, []workspace.Edit{
		{OldText: `<span class="live">`, NewText: `<span class="live done">`},
		{OldText: button, NewText: strings.Replace(button, "10px", "999px", 1)},
	}, claude("agent:bob"), "按钮圆一点")
	var conflict *workspace.EditConflictError
	if !errors.As(err, &conflict) || conflict.Index != 2 || conflict.Occurrences != 0 || conflict.Latest != 3 {
		t.Fatalf("an edit of text that changed: %v", err)
	}
	if page, _ := f.store.Page(ctx, page.ID); page.Latest.Version != 3 {
		t.Fatalf("a refused edit saved v%d", page.Latest.Version)
	}

	for name, edits := range map[string][]workspace.Edit{
		"ambiguous": {{OldText: "<section", NewText: "<section "}},
		"folded":    {{OldText: "data:image/png;base64,…(48 KB, #1)", NewText: ""}},
		"no change": {{OldText: "已通过", NewText: "已通过"}},
		"empty":     {{OldText: "", NewText: "x"}},
		"none":      {},
	} {
		if _, err := f.store.EditPage(ctx, f.bob, page.ID, 3, edits, claude("agent:bob"), ""); err == nil {
			t.Errorf("%s edit was saved", name)
		}
	}
	if _, err := f.store.EditPage(ctx, f.bob, "page_missing", 1, []workspace.Edit{{OldText: "a", NewText: "b"}}, claude("agent:bob"), ""); !errors.Is(err, workspace.ErrNotFound) {
		t.Errorf("editing a missing page: %v", err)
	}
}

func TestOnlyTheUploaderAndAdministratorsDeleteAPage(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	page, err := f.store.CreatePage(ctx, f.alice, "a.html", []byte("<title>A</title>"), workspace.Web, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RenamePage(ctx, f.bob, page.ID, "  新  标题 "); err != nil {
		t.Fatalf("anyone may rename: %v", err)
	}
	if _, err := f.store.RenamePage(ctx, f.bob, page.ID, strings.Repeat("长", workspace.MaxTitleRunes+1)); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatalf("a long title: %v", err)
	}
	if err := f.store.DeletePage(ctx, f.bob, page.ID); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("another member deleted the page: %v", err)
	}
	if err := f.store.DeletePage(ctx, f.admin, page.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Page(ctx, page.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("a deleted page: %v", err)
	}
	other, _ := f.store.CreatePage(ctx, f.bob, "b.html", []byte("<p>b"), workspace.Web, "")
	if err := f.store.DeletePage(ctx, f.bob, other.ID); err != nil {
		t.Fatalf("the uploader deletes: %v", err)
	}
}

func TestAdministratorsChooseWhoUsesTheWorkspace(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	if ok, _ := f.store.CanUse(ctx, f.admin); !ok {
		t.Fatal("an administrator cannot use the workspace")
	}
	if ok, _ := f.store.CanUse(ctx, f.alice); ok {
		t.Fatal("a member uses the workspace before being added")
	}
	if err := f.store.AddMember(ctx, f.bob, f.alice.AccountID); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("a member added another: %v", err)
	}
	for range 2 {
		if err := f.store.AddMember(ctx, f.admin, f.alice.AccountID); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := f.store.CanUse(ctx, f.alice); !ok {
		t.Fatal("an added member cannot use the workspace")
	}
	if members, err := f.store.Members(ctx); err != nil || len(members) != 1 || members[0].AddedBy != f.admin.AccountID {
		t.Fatalf("Members() = %+v, %v", members, err)
	}
	events, err := f.store.Activity(ctx, f.admin, activity.Query{Limit: 10})
	if err != nil || len(events) != 1 || events[0].Action != "workspace.member_added" {
		t.Fatalf("adding twice records %+v, %v", events, err)
	}
	if err := f.store.RemoveMember(ctx, f.admin, f.alice.AccountID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.store.CanUse(ctx, f.alice); ok {
		t.Fatal("a removed member still uses the workspace")
	}
}

func TestATransferLinkWorksOnceAndOnlyForTenMinutes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	page, _ := f.store.CreatePage(ctx, f.alice, "a.html", []byte("<p>a"), workspace.Web, "")
	if _, _, err := f.store.CreateTransfer(ctx, workspace.Transfer{Direction: workspace.Download, PageID: page.ID, Actor: f.alice, Via: workspace.Web}); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatalf("a link without an agent token: %v", err)
	}
	key, expires, err := f.store.CreateTransfer(ctx, workspace.Transfer{Direction: workspace.Upload, PageID: page.ID, Version: 1, Actor: f.alice, Via: claude("agent:a"), Note: "重写"})
	if err != nil || !expires.Equal(f.now.Add(workspace.TransferLife)) {
		t.Fatalf("CreateTransfer() = %v, %v", expires, err)
	}
	if _, err := f.store.TakeTransfer(ctx, key, workspace.Download); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("an upload link used to download: %v", err)
	}
	transfer, err := f.store.TakeTransfer(ctx, key, workspace.Upload)
	if err != nil || transfer.PageID != page.ID || transfer.Version != 1 || transfer.Via.TokenID != "agent:a" || transfer.Actor.Name != "Alice" || transfer.Note != "重写" {
		t.Fatalf("TakeTransfer() = %+v, %v", transfer, err)
	}
	if _, err := f.store.TakeTransfer(ctx, key, workspace.Upload); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("a link used twice: %v", err)
	}
	late, _, _ := f.store.CreateTransfer(ctx, workspace.Transfer{Direction: workspace.Download, PageID: page.ID, Actor: f.alice, Via: claude("agent:a")})
	*f.now = f.now.Add(workspace.TransferLife)
	if _, err := f.store.TakeTransfer(ctx, late, workspace.Download); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("an expired link: %v", err)
	}
	if _, _, err := f.store.CreateTransfer(ctx, workspace.Transfer{Direction: workspace.Download, Actor: f.alice, Via: claude("agent:a")}); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatalf("a download without a page: %v", err)
	}
}

func TestASelectionIsTheMembersOwnForHalfAnHour(t *testing.T) {
	f := newFixture(t)
	f.store.SetViewing(f.alice.AccountID, "page_a", 2)
	f.store.SetSelection(f.alice.AccountID, workspace.Selection{PageID: "page_a", Version: 2, At: 10, Tag: "button"})
	if _, ok := f.store.Selection(f.bob.AccountID); ok {
		t.Fatal("bob sees alice's selection")
	}
	if selection, ok := f.store.Selection(f.alice.AccountID); !ok || selection.Tag != "button" {
		t.Fatalf("Selection() = %+v, %v", selection, ok)
	}
	*f.now = f.now.Add(workspace.SelectionLife)
	if _, ok := f.store.Selection(f.alice.AccountID); ok {
		t.Fatal("a selection outlived half an hour")
	}
	if viewing, ok := f.store.Viewing(f.alice.AccountID); !ok || viewing.PageID != "page_a" || viewing.Version != 2 {
		t.Fatalf("Viewing() = %+v, %v", viewing, ok)
	}
}

func TestADeletedAccountLeavesItsPagesUnderItsName(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	if err := f.store.AddMember(ctx, f.admin, f.alice.AccountID); err != nil {
		t.Fatal(err)
	}
	page, _ := f.store.CreatePage(ctx, f.alice, "a.html", []byte("<p>a"), workspace.Web, "")
	f.store.SetViewing(f.alice.AccountID, page.ID, 1)
	if err := f.store.RemoveAccount(ctx, f.alice.AccountID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.store.CanUse(ctx, f.alice); ok {
		t.Fatal("a deleted account is still a member")
	}
	if _, ok := f.store.Viewing(f.alice.AccountID); ok {
		t.Fatal("a deleted account is still looking at a page")
	}
	if kept, err := f.store.Page(ctx, page.ID); err != nil || kept.CreatedBy.Name != "Alice" {
		t.Fatalf("the page = %+v, %v", kept, err)
	}
}
