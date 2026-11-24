package montage

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// A version's outline is the film as it is (#616 review): it is
// read without making a draft, and an edit not saved yet does not show.
func TestAVersionsOutlineIsItsFilmNotItsDraft(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	film := []string{"c1/1", "c2/1", "c2/2", "c3/2"}
	outlined := func() []string {
		t.Helper()
		outline, err := env.store.VersionOutline(ctx, editOwner, env.version)
		if err != nil {
			t.Fatal(err)
		}
		if outline.VersionNumber != 1 || outline.Plan.Design != "从海边走到山里" || len(outline.Parts) != 2 || outline.Seconds != 14 ||
			outline.Counts != (DraftCounts{Shots: 4, Videos: 2, Portrait: 1}) || !slices.Equal(outline.Unmet, []string{"直升机", "雪地", "日落"}) {
			t.Fatalf("the outline = %+v", outline)
		}
		var beats []string
		for _, shot := range outline.Shots {
			if shot.Changed || shot.Extended || shot.Locked {
				t.Fatalf("a shot of the film = %+v", shot)
			}
			beats = append(beats, shot.Beat)
		}
		return beats
	}
	if got := outlined(); !slices.Equal(got, film) {
		t.Fatalf("the film's shots = %v", got)
	}
	if n := env.store.Count(t, "SELECT COUNT(*) FROM drafts"); n != 0 {
		t.Fatalf("reading the outline made %d drafts", n)
	}
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []DraftOp{{Type: "replace", Shot: 1, Beat: "c1/3"}, {Type: "lock", Shot: 2}, {Type: "delete", Shot: 4}} {
		if draft, err = env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, op); err != nil {
			t.Fatal(err)
		}
	}
	if slices.Equal(beats(draft), film) {
		t.Fatal("the draft was not edited")
	}
	if got := outlined(); !slices.Equal(got, film) {
		t.Fatalf("the film's shots after editing its draft = %v", got)
	}
	if _, err := env.store.VersionOutline(ctx, editOther, env.version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's outline = %v", err)
	}
}

// What the plan did not find is a list of texts; anything else says nothing.
func TestAnUnmetThatIsNotAListSaysNothing(t *testing.T) {
	src := &editSource{picked: pickedDoc{Unmet: json.RawMessage(`"直升机"`)}}
	if got := src.unmet(); got == nil || len(got) != 0 {
		t.Fatalf("unmet = %#v", got)
	}
}
