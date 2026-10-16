package photos_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

func TestTurningPeopleOffClearsWhatWasRecognisedAndKeepsNames(t *testing.T) {
	ctx := context.Background()
	service, root := newPeopleService(t)
	private, shared := libraries(t, service, alice)
	worker := newFacingEmbedder()
	byName := make(map[string]photos.Asset)
	for i, painted := range []int{0, 0, 0, 0, 1, 1, 1} {
		name := string(rune('a'+i)) + ".png"
		byName[name] = importPhoto(t, service, alice, private.ID, "", name, peoplePhoto(t, uint8(i), painted))
	}
	organise(t, service, worker)
	found := people(t, service, alice, false)
	if len(found) != 2 {
		t.Fatalf("People() = %+v; want red and green", found)
	}
	red := found[0]
	name := "小明"
	if _, err := service.UpdatePerson(ctx, alice, red.ID, photos.PersonChange{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveFromPerson(ctx, alice, red.ID, byName["d.png"].ID); err != nil {
		t.Fatal(err)
	}
	// One photo is also in the shared library, which keeps people on.
	copied, err := service.Copy(ctx, alice, byName["b.png"].ID, shared.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	organise(t, service, worker)
	before, err := service.Get(ctx, alice, byName["a.png"].ID)
	if err != nil || len(before.People) != 1 {
		t.Fatalf("a.png's people = %+v, %v", before.People, err)
	}

	// Only the owner of a private library, and an administrator for the
	// shared one, may turn people off.
	if _, err := service.SetPeople(ctx, bob, private.ID, false); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("bob's SetPeople(alice's library) error = %v, want not found", err)
	}
	if _, err := service.SetPeople(ctx, alice, shared.ID, false); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("alice's SetPeople(shared) error = %v, want forbidden", err)
	}
	lib, err := service.SetPeople(ctx, alice, private.ID, false)
	if err != nil || lib.People || !lib.CanSetPeople {
		t.Fatalf("SetPeople(off) = %+v, %v", lib, err)
	}
	if got := people(t, service, alice, true); len(got) != 0 {
		t.Fatalf("People() with people off = %+v", got)
	}
	if _, err := service.PersonPhotos(ctx, alice, red.ID, "", 10); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("PersonPhotos() with people off error = %v, want not found", err)
	}
	details, err := service.Get(ctx, alice, byName["a.png"].ID)
	if err != nil || len(details.People) != 0 {
		t.Fatalf("a.png's people with people off = %+v, %v", details.People, err)
	}
	if page := search(t, service, alice, "小明"); len(page.People) != 0 {
		t.Fatalf("Search(小明) with people off keeps to %v", page.People)
	}
	// The faces and avatars of photos only the private library holds are
	// gone; the shared library's copy keeps its own.
	sheets, err := filepath.Glob(filepath.Join(root, "derived", "faces-v1", "*", "*.jpg"))
	if err != nil || len(sheets) != 1 {
		t.Fatalf("face sheets = %v, %v; want only the shared copy's", sheets, err)
	}
	kept, err := service.Get(ctx, alice, copied.ID)
	if err != nil || len(kept.People) != 1 {
		t.Fatalf("the shared copy's people = %+v, %v", kept.People, err)
	}
	if _, err := service.OccurrenceAvatar(ctx, alice, kept.People[0].OccurrenceID); err != nil {
		t.Fatalf("OccurrenceAvatar() of the shared copy's face error = %v", err)
	}
	if _, err := service.OccurrenceAvatar(ctx, alice, before.People[0].OccurrenceID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("OccurrenceAvatar() of a cleared face error = %v, want not found", err)
	}
	// Nothing is scanned for it any more, and its photos count as organised.
	scanned := len(worker.faceInputs)
	organise(t, service, worker)
	importPhoto(t, service, alice, private.ID, "", "h.png", peoplePhoto(t, 7, 0))
	organise(t, service, worker)
	if len(worker.faceInputs) != scanned {
		t.Fatalf("scanned %d faces with people off", len(worker.faceInputs)-scanned)
	}
	status, err := service.AIStatus(ctx, alice)
	if err != nil || status.Pending != 0 || status.Ready != 8 {
		t.Fatalf("AIStatus() = %+v, %v; want every photo organised", status, err)
	}

	// On again: every photo is scanned again; the name and what alice
	// decided come back with the faces.
	if lib, err := service.SetPeople(ctx, alice, private.ID, true); err != nil || !lib.People {
		t.Fatalf("SetPeople(on) = %+v, %v", lib, err)
	}
	organise(t, service, worker)
	if len(worker.faceInputs) != scanned+7 {
		t.Fatalf("scanned %d photos after turning on, want the 7 without a scan", len(worker.faceInputs)-scanned)
	}
	found = people(t, service, alice, false)
	if len(found) != 2 || found[0].ID != red.ID || found[0].Name != "小明" {
		t.Fatalf("People() after turning on = %+v; want 小明 kept", found)
	}
	if got := personPhotos(t, service, alice, red.ID); !slices.Equal(got, []string{"a.png", "b.png", "c.png", "h.png"}) {
		t.Fatalf("小明's photos after turning on = %v; d.png stays out", got)
	}
	all, err := service.Libraries(ctx, alice)
	if err != nil || !all[0].People || !all[0].CanSetPeople || !all[1].People || all[1].CanSetPeople {
		t.Fatalf("Libraries() = %+v, %v", all, err)
	}
	if all, err := service.Libraries(ctx, admin); err != nil || !all[1].CanSetPeople {
		t.Fatalf("admin's Libraries() = %+v, %v; want the shared switch theirs", all, err)
	}
}

// TestAnotherMembersPeopleAreIndistinguishableFromNothing checks every path a
// member has into another member's people: each fails exactly as it would for
// IDs that do not exist, and their names find nothing.
func TestAnotherMembersPeopleAreIndistinguishableFromNothing(t *testing.T) {
	ctx := context.Background()
	service, _ := newPeopleService(t)
	private, _ := libraries(t, service, alice)
	worker := newFacingEmbedder()
	var photo photos.Asset
	for i := range 3 {
		photo = importPhoto(t, service, alice, private.ID, "", string(rune('a'+i))+".png", peoplePhoto(t, uint8(i), 0))
	}
	organise(t, service, worker)
	found := people(t, service, alice, false)
	if len(found) != 1 {
		t.Fatalf("People() = %+v", found)
	}
	person := found[0]
	name := "小明"
	if _, err := service.UpdatePerson(ctx, alice, person.ID, photos.PersonChange{Name: &name}); err != nil {
		t.Fatal(err)
	}
	details, err := service.Get(ctx, alice, photo.ID)
	if err != nil || len(details.People) != 1 {
		t.Fatalf("Get() = %+v, %v", details.People, err)
	}
	occurrence := details.People[0].OccurrenceID

	probes := map[string]func(library, person, asset, occurrence string) error{
		"person photos": func(_, p, _, _ string) error { _, err := service.PersonPhotos(ctx, bob, p, "", 10); return err },
		"rename": func(_, p, _, _ string) error {
			_, err := service.UpdatePerson(ctx, bob, p, photos.PersonChange{Name: &name})
			return err
		},
		"merge":        func(_, p, _, _ string) error { _, err := service.MergePeople(ctx, bob, p, "person:none"); return err },
		"remove":       func(_, p, a, _ string) error { return service.RemoveFromPerson(ctx, bob, p, a) },
		"assign":       func(_, _, a, o string) error { _, err := service.AssignOccurrence(ctx, bob, a, o, "", "x"); return err },
		"avatar":       func(_, _, _, o string) error { _, err := service.OccurrenceAvatar(ctx, bob, o); return err },
		"switch":       func(l, _, _, _ string) error { _, err := service.SetPeople(ctx, bob, l, false); return err },
		"admin avatar": func(_, _, _, o string) error { _, err := service.OccurrenceAvatar(ctx, admin, o); return err },
	}
	for probeName, probe := range probes {
		hidden := probe(private.ID, person.ID, photo.ID, occurrence)
		missing := probe("library:missing", "person:missing", "photo:missing", "face:missing")
		if !errors.Is(hidden, photos.ErrNotFound) || hidden.Error() != missing.Error() {
			t.Errorf("%s: hidden error %v, missing error %v; want the same not-found", probeName, hidden, missing)
		}
	}
	if got := people(t, service, bob, true); len(got) != 0 {
		t.Fatalf("bob's People() = %+v", got)
	}
	if page := search(t, service, bob, "小明"); len(page.People) != 0 || len(page.Assets) != 0 {
		t.Fatalf("bob's Search(小明) = %v, people %v", page.Assets, page.People)
	}
	// Alice's people are as she left them.
	if found := people(t, service, alice, false); len(found) != 1 || found[0].Name != "小明" || found[0].Photos != 3 {
		t.Fatalf("alice's People() = %+v", found)
	}
}
