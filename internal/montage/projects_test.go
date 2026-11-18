package montage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// library stands in for the photo library's grants: each session is a
// member, each video belongs to one, and a grant reaches its project's.
type library struct {
	mu        sync.Mutex
	down      bool
	videos    map[string]video
	grants    map[string]*grant
	registers int
	revoked   []string
}

type video struct {
	owner, object string
	trashed       bool
	takenAt       time.Time
}

type grant struct {
	photos.Grant
	assets  map[string]bool
	revoked bool
}

func newLibrary() *library {
	return &library{videos: map[string]video{}, grants: map[string]*grant{}}
}

func refused(code string) error { return &photosapi.GrantError{Code: code, Message: code} }

func (l *library) Register(_ context.Context, session, projectID string, ids []string) (photos.Grant, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return photos.Grant{}, errors.New("photo grant socket: connection refused")
	}
	owner := "user:" + session
	for _, id := range ids {
		if v, ok := l.videos[id]; !ok || v.owner != owner || v.trashed {
			return photos.Grant{}, refused(photosapi.GrantAssetsRefused)
		}
	}
	l.registers++
	var found *grant
	for _, g := range l.grants {
		if g.OwnerUserID == owner && g.ProjectID == projectID {
			found = g
		}
	}
	if found == nil {
		found = &grant{ID: fmt.Sprintf("grant:%d", len(l.grants)+1), OwnerUserID: owner, ProjectID: projectID, assets: map[string]bool{}}
		l.grants[found.ID] = found
	}
	for _, id := range ids {
		found.assets[id] = true
	}
	return found.Grant, nil
}

func (l *library) Lookup(_ context.Context, grantID string, ids []string) ([]photos.GrantedAsset, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return nil, errors.New("photo grant socket: connection refused")
	}
	g, ok := l.grants[grantID]
	if !ok || g.revoked {
		return nil, refused(photosapi.GrantNotFound)
	}
	var out []photos.GrantedAsset
	for _, id := range ids {
		v, ok := l.videos[id]
		switch {
		case !ok || !g.assets[id]:
			out = append(out, photos.GrantedAsset{AssetID: id, Status: photos.GrantMissing})
		case v.trashed:
			out = append(out, photos.GrantedAsset{AssetID: id, Status: photos.GrantTrashed})
		default:
			taken := v.takenAt
			out = append(out, photos.GrantedAsset{AssetID: id, Status: photos.GrantAvailable, ObjectID: v.object, Duration: 10, TakenAt: &taken})
		}
	}
	return out, nil
}

func (l *library) Remove(context.Context, string, []string) error { return nil }

func (l *library) List(context.Context) ([]photos.Grant, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return nil, errors.New("photo grant socket: connection refused")
	}
	var out []photos.Grant
	for _, g := range l.grants {
		if !g.revoked {
			out = append(out, g.Grant)
		}
	}
	return out, nil
}

func (l *library) Revoke(_ context.Context, grantID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return errors.New("photo grant socket: connection refused")
	}
	if g, ok := l.grants[grantID]; ok {
		g.revoked = true
	}
	l.revoked = append(l.revoked, grantID)
	return nil
}

func (l *library) add(id, owner, object string, day int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.videos[id] = video{owner: owner, object: object, takenAt: time.Date(2026, 8, day, 12, 0, 0, 0, time.Local)}
}

func openStore(t *testing.T, root string, grants montage.Grants) *montage.Store {
	t.Helper()
	store, err := montage.Open(context.Background(), root, montage.Options{Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newStore(t *testing.T) (*montage.Store, *library, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "montage")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lib := newLibrary()
	lib.add("asset:a1", "user:alice", "object:1", 2)
	lib.add("asset:a2", "user:alice", "object:2", 4)
	lib.add("asset:a3", "user:alice", "object:3", 5)
	lib.add("asset:b1", "user:bob", "object:9", 3)
	return openStore(t, root, lib), lib, root
}

func TestAProjectHoldsOnlyTheVideosHandedToIt(t *testing.T) {
	ctx := context.Background()
	store, lib, _ := newStore(t)
	project, created, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1", "asset:a2"})
	if err != nil || !created {
		t.Fatalf("CreateProject() = %+v, %v, %v", project, created, err)
	}
	if project.Name != "2026-08-02 至 08-04" || project.AssetCount != 2 || project.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("project = %+v", project)
	}
	// The same request again: one project, one grant, no second registration.
	again, created, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1", "asset:a2"})
	if err != nil || created || again.ID != project.ID || lib.registers != 1 || len(lib.grants) != 1 {
		t.Fatalf("again = %+v, created %v, %v; registers %d, grants %d", again, created, err, lib.registers, len(lib.grants))
	}
	// Bob's same request key is his own: he gets no project of Alice's.
	if _, _, err := store.CreateProject(ctx, "user:bob", "bob", "key:1", "", []string{"asset:a1"}); !errors.Is(err, montage.ErrAssetsRefused) {
		t.Fatalf("Bob with Alice's video = %v", err)
	}
	if _, err := store.Project(ctx, "user:bob", project.ID); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("Bob reading Alice's project = %v", err)
	}
	if page, _ := store.Projects(ctx, "user:bob", ""); len(page.Items) != 0 {
		t.Fatalf("Bob's projects = %+v", page)
	}

	// No videos is not all of them, and 501 is too many.
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:2", "", nil); !errors.Is(err, montage.ErrInvalid) {
		t.Fatalf("CreateProject(no videos) = %v", err)
	}
	many := make([]string, montage.MaxAssets+1)
	for i := range many {
		many[i] = fmt.Sprintf("asset:%d", i)
	}
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:3", "", many); !errors.Is(err, montage.ErrTooManyAssets) {
		t.Fatalf("CreateProject(501) = %v", err)
	}

	// Adding to the project adds its videos and nothing else.
	added, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:4", []string{"asset:a3"})
	if err != nil || added.AssetCount != 3 {
		t.Fatalf("AddAssets() = %+v, %v", added, err)
	}
	// The same key again adds nothing more; another project's key is refused.
	if again, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:4", []string{"asset:a3"}); err != nil || again.AssetCount != 3 {
		t.Fatalf("AddAssets() again = %+v, %v", again, err)
	}
	lib.add("asset:a4", "user:alice", "object:4", 6)
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:5", "", []string{"asset:a4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:5", []string{"asset:a3"}); !errors.Is(err, montage.ErrConflict) {
		t.Fatalf("AddAssets(another project's key) = %v", err)
	}

	// A video in the trash, and one whose content changed, show as such.
	lib.mu.Lock()
	lib.videos["asset:a1"] = video{owner: "user:alice", object: "object:1", trashed: true}
	lib.videos["asset:a2"] = video{owner: "user:alice", object: "object:changed"}
	lib.mu.Unlock()
	detail, err := store.Project(ctx, "user:alice", project.ID)
	statuses := []montage.AssetStatus{}
	for _, asset := range detail.Assets {
		statuses = append(statuses, asset.Status)
	}
	if err != nil || !slices.Equal(statuses, []montage.AssetStatus{montage.AssetTrashed, montage.AssetMissing, montage.AssetAvailable}) {
		t.Fatalf("statuses = %v, %v", statuses, err)
	}

	// Renamed, with a request; then deleted: the grant is revoked.
	name, ask := "海边", "节奏轻快"
	if detail, err = store.UpdateProject(ctx, "user:alice", project.ID, montage.ProjectChange{Name: &name, Ask: &ask}); err != nil || detail.Name != name || detail.Ask != ask {
		t.Fatalf("UpdateProject() = %+v, %v", detail, err)
	}
	if err := store.DeleteProject(ctx, "user:bob", project.ID); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("Bob deleting Alice's project = %v", err)
	}
	if err := store.DeleteProject(ctx, "user:alice", project.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(lib.revoked, "grant:1") {
		t.Fatalf("revoked = %v", lib.revoked)
	}
	if _, err := store.Project(ctx, "user:alice", project.ID); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("a deleted project = %v", err)
	}
}

func TestARevocationThePhotoLibraryMissedIsTriedAgain(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "montage")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lib := newLibrary()
	lib.add("asset:a1", "user:alice", "object:1", 2)
	store, err := montage.Open(ctx, root, montage.Options{Grants: lib, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	project, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1"})
	if err != nil {
		t.Fatal(err)
	}
	lib.down = true
	if err := store.DeleteProject(ctx, "user:alice", project.ID); err != nil {
		t.Fatal(err)
	}
	lib.down = false
	if err := store.RetryRevocations(ctx); err != nil || len(lib.revoked) != 0 {
		t.Fatalf("retried before its time: %v, %v", lib.revoked, err)
	}
	clock = clock.Add(2 * time.Minute)
	if err := store.RetryRevocations(ctx); err != nil || !slices.Equal(lib.revoked, []string{"grant:1"}) {
		t.Fatalf("revoked = %v, %v", lib.revoked, err)
	}
}

func TestReconcilingFollowsWhatThePhotoLibraryHolds(t *testing.T) {
	ctx := context.Background()
	store, lib, root := newStore(t)
	kept, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1"})
	if err != nil {
		t.Fatal(err)
	}
	lost, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:2", "", []string{"asset:a2"})
	if err != nil {
		t.Fatal(err)
	}
	// A restart after the grant was registered and before the project was
	// made: the photo library registers, then the service stops.
	lib.down = true
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:3", "", []string{"asset:a3"}); !errors.Is(err, montage.ErrPhotosUnavailable) {
		t.Fatalf("CreateProject() while the photo library is away = %v", err)
	}
	lib.down = false
	if _, err := lib.Register(ctx, "alice", "project:orphan", []string{"asset:a3"}); err != nil {
		t.Fatal(err)
	}
	// The Catalog was restored to before the second project's grant.
	lib.mu.Lock()
	for id, g := range lib.grants {
		if g.ProjectID == lost.ID {
			delete(lib.grants, id)
		}
	}
	lib.mu.Unlock()

	_ = store.Close()
	store = openStore(t, root, lib)
	if err := store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if detail, err := store.Project(ctx, "user:alice", kept.ID); err != nil || detail.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("kept project = %+v, %v", detail, err)
	}
	if detail, err := store.Project(ctx, "user:alice", lost.ID); err != nil || detail.Assets[0].Status != montage.AssetMissing {
		t.Fatalf("project whose grant is gone = %+v, %v", detail, err)
	}
	page, err := store.Projects(ctx, "user:alice", "")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("projects after reconciling = %+v, %v; want the unfinished one gone", page, err)
	}
	var orphan string
	for id, g := range lib.grants {
		if g.ProjectID == "project:orphan" {
			orphan = id
		}
	}
	if !slices.Contains(lib.revoked, orphan) {
		t.Fatalf("revoked = %v; want the grant no project holds (%s)", lib.revoked, orphan)
	}
	// Retrying the unfinished request makes one project.
	if _, created, err := store.CreateProject(ctx, "user:alice", "alice", "key:3", "", []string{"asset:a3"}); err != nil || !created {
		t.Fatalf("retrying = %v, %v", created, err)
	}
}

func TestProjectsComePageByPage(t *testing.T) {
	ctx := context.Background()
	store, lib, _ := newStore(t)
	for i := range 51 {
		id := fmt.Sprintf("asset:p%d", i)
		lib.add(id, "user:alice", "object:p"+id, 2)
		if _, _, err := store.CreateProject(ctx, "user:alice", "alice", fmt.Sprintf("key:%d", i), "", []string{id}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Projects(ctx, "user:alice", "")
	if err != nil || len(first.Items) != 50 || first.Next == "" {
		t.Fatalf("first page = %d items, next %q, %v", len(first.Items), first.Next, err)
	}
	second, err := store.Projects(ctx, "user:alice", first.Next)
	if err != nil || len(second.Items) != 1 || second.Next != "" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}
