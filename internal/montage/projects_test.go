package montage_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// library stands in for the photo library's grants: each session is a
// member, each video belongs to one, and a grant reaches its project's. As
// the photo library's, its list leaves revoked grants out, a revoked grant
// answers grant_not_found and a project whose grant was revoked cannot
// register again.
type library struct {
	mu        sync.Mutex
	down      bool
	inactive  bool
	videos    map[string]video
	grants    map[string]*grant
	next      int
	registers int
	revoked   []string
	// What jobs use (jobs_test.go): each video's analysis state (done
	// when absent), the films saved by version key, and faults.
	states   map[string]photos.AnalysisState
	films    map[string]string
	saves    int
	noSpace  bool
	lostSave int
	retried  []string
	removed  []string
	// asked are the videos an analysis was asked for, other than again.
	asked []string
	// described, when set, changes a video's analysis before it is read.
	described func(assetID string, analysis *photos.GrantAnalysis)
	// saving, when set, runs when a film arrives, outside the lock.
	saving func()
	// registered, when set, runs after a registration, outside the lock.
	registered func()
}

// A video lasts ten seconds unless seconds says otherwise.
type video struct {
	owner, object string
	trashed       bool
	takenAt       time.Time
	seconds       float64
}

type grant struct {
	photos.Grant
	assets  map[string]bool
	revoked bool
}

func newLibrary() *library {
	return &library{videos: map[string]video{}, grants: map[string]*grant{}, states: map[string]photos.AnalysisState{}, films: map[string]string{}}
}

func refused(code string) error { return &photosapi.GrantError{Code: code, Message: code} }

var errDown = errors.New("photo grant socket: connection refused")

func (l *library) setDown(down bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.down = down
}

func (l *library) Register(_ context.Context, session, projectID string, ids []string) (photos.Grant, error) {
	l.mu.Lock()
	if l.down {
		l.mu.Unlock()
		return photos.Grant{}, errDown
	}
	owner := "user:" + session
	for _, id := range ids {
		if v, ok := l.videos[id]; !ok || v.owner != owner || v.trashed {
			l.mu.Unlock()
			return photos.Grant{}, refused(photosapi.GrantAssetsRefused)
		}
	}
	var found *grant
	for _, g := range l.grants {
		if g.OwnerUserID == owner && g.ProjectID == projectID {
			found = g
		}
	}
	if found != nil && found.revoked {
		l.mu.Unlock()
		return photos.Grant{}, refused(photosapi.GrantNotFound)
	}
	if found == nil {
		l.next++
		found = &grant{ID: fmt.Sprintf("grant:%d", l.next), OwnerUserID: owner, ProjectID: projectID, assets: map[string]bool{}}
		l.grants[found.ID] = found
	}
	for _, id := range ids {
		found.assets[id] = true
	}
	l.registers++
	registered, answer := l.registered, found.Grant
	l.mu.Unlock()
	if registered != nil {
		registered()
	}
	return answer, nil
}

func (l *library) usable(grantID string) (*grant, error) {
	if l.down {
		return nil, errDown
	}
	if l.inactive {
		return nil, refused(photosapi.GrantOwnerInactive)
	}
	g, ok := l.grants[grantID]
	if !ok || g.revoked {
		return nil, refused(photosapi.GrantNotFound)
	}
	return g, nil
}

func (l *library) Lookup(_ context.Context, grantID string, ids []string) ([]photos.GrantedAsset, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, err := l.usable(grantID)
	if err != nil {
		return nil, err
	}
	if len(ids) > photos.MaxGrantAssets {
		return nil, refused(photosapi.GrantTooMany)
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
			state, ok := l.states[id]
			if !ok {
				state = photos.AnalysisDone
			}
			seconds := 10.0
			if v.seconds > 0 {
				seconds = v.seconds
			}
			out = append(out, photos.GrantedAsset{AssetID: id, Status: photos.GrantAvailable, ObjectID: v.object, Duration: seconds, TakenAt: &taken,
				Width: 1920, Height: 1080, Windows: state, Cards: state, Quality: state, Descriptions: state})
		}
	}
	return out, nil
}

func (l *library) Remove(_ context.Context, grantID string, ids []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, err := l.usable(grantID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		delete(g.assets, id)
	}
	l.removed = append(l.removed, ids...)
	return nil
}

func (l *library) List(context.Context) ([]photos.Grant, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return nil, errDown
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
		return errDown
	}
	if g, ok := l.grants[grantID]; ok && !g.revoked {
		g.revoked, g.assets = true, map[string]bool{}
	}
	l.revoked = append(l.revoked, grantID)
	return nil
}

func (l *library) add(id, owner, object string, day int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.videos[id] = video{owner: owner, object: object, takenAt: time.Date(2026, 8, day, 12, 0, 0, 0, time.Local)}
}

func (l *library) trash(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.videos[id]
	v.trashed = true
	l.videos[id] = v
}

// grantOf returns the project's grant, revoked or not.
func (l *library) grantOf(projectID string) (grant, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, g := range l.grants {
		if g.ProjectID == projectID {
			return grant{Grant: g.Grant, assets: maps.Clone(g.assets), revoked: g.revoked}, true
		}
	}
	return grant{}, false
}

// forget drops the project's grant, as a Catalog restored to before it.
func (l *library) forget(projectID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, g := range l.grants {
		if g.ProjectID == projectID {
			delete(l.grants, id)
		}
	}
}

func (l *library) revokedGrants() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.revoked)
}

func (l *library) registrations() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.registers
}

// clock is a store's time, moved by the test.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func openStore(t *testing.T, root string, grants montage.Grants, now func() time.Time) *montage.Store {
	t.Helper()
	store, err := montage.Open(context.Background(), root, montage.Options{Grants: grants, Now: now})
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
	return openStore(t, root, lib, nil), lib, root
}

func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(time.Millisecond)
	}
}

func statuses(detail montage.ProjectDetail) []montage.AssetStatus {
	out := []montage.AssetStatus{}
	for _, asset := range detail.Assets {
		out = append(out, asset.Status)
	}
	return out
}

func TestAProjectHoldsOnlyTheVideosHandedToIt(t *testing.T) {
	ctx := context.Background()
	store, lib, _ := newStore(t)
	project, created, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a2", "asset:a1"})
	if err != nil || !created {
		t.Fatalf("CreateProject() = %+v, %v, %v", project, created, err)
	}
	if project.Name != "2026-08-02 至 08-04" || project.AssetCount != 2 || project.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("project = %+v", project)
	}
	// The same request again, in any order: one project, one grant, no
	// second registration.
	again, created, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1", "asset:a2", "asset:a1"})
	if err != nil || created || again.ID != project.ID || lib.registrations() != 1 || len(lib.grants) != 1 {
		t.Fatalf("again = %+v, created %v, %v; registers %d, grants %d", again, created, err, lib.registrations(), len(lib.grants))
	}
	// The key repeats that request alone.
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a3"}); !errors.Is(err, montage.ErrConflict) {
		t.Fatalf("CreateProject(the key with other videos) = %v", err)
	}
	if _, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:1", []string{"asset:a1", "asset:a2"}); !errors.Is(err, montage.ErrConflict) {
		t.Fatalf("AddAssets(a creation's key) = %v", err)
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
	for _, id := range []string{"asset 1", "asset:é", fmt.Sprintf("%065d", 0)} {
		if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:4", "", []string{id}); !errors.Is(err, montage.ErrInvalid) {
			t.Fatalf("CreateProject(%q) = %v", id, err)
		}
	}

	// Adding to the project adds its videos and nothing else.
	added, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:5", []string{"asset:a3"})
	if err != nil || added.AssetCount != 3 {
		t.Fatalf("AddAssets() = %+v, %v", added, err)
	}
	// The same key again adds nothing more, and with other videos it is
	// another request.
	if again, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:5", []string{"asset:a3"}); err != nil || again.AssetCount != 3 {
		t.Fatalf("AddAssets() again = %+v, %v", again, err)
	}
	lib.add("asset:a4", "user:alice", "object:4", 6)
	if _, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:5", []string{"asset:a4"}); !errors.Is(err, montage.ErrConflict) {
		t.Fatalf("AddAssets(the key with other videos) = %v", err)
	}
	if detail, err := store.Project(ctx, "user:alice", project.ID); err != nil || detail.AssetCount != 3 {
		t.Fatalf("after a conflicting key: %+v, %v", detail, err)
	}
	other, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:6", "", []string{"asset:a4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:6", []string{"asset:a4"}); !errors.Is(err, montage.ErrConflict) {
		t.Fatalf("AddAssets(another project's key) = %v", err)
	}
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:5", "", []string{"asset:a3"}); !errors.Is(err, montage.ErrConflict) {
		t.Fatalf("CreateProject(an addition's key) = %v", err)
	}

	// A video in the trash, and one whose content changed, show as such.
	lib.trash("asset:a1")
	lib.mu.Lock()
	lib.videos["asset:a2"] = video{owner: "user:alice", object: "object:changed"}
	lib.mu.Unlock()
	detail, err := store.Project(ctx, "user:alice", project.ID)
	if got := statuses(detail); err != nil || !slices.Equal(got, []montage.AssetStatus{montage.AssetTrashed, montage.AssetMissing, montage.AssetAvailable}) {
		t.Fatalf("statuses = %v, %v", got, err)
	}
	// An owner whose account is not in use answers nothing about them yet.
	lib.mu.Lock()
	lib.inactive = true
	lib.mu.Unlock()
	if detail, err := store.Project(ctx, "user:alice", project.ID); err != nil || !slices.Equal(statuses(detail), []montage.AssetStatus{montage.AssetUnknown, montage.AssetUnknown, montage.AssetUnknown}) {
		t.Fatalf("statuses while the owner is inactive = %v, %v", statuses(detail), err)
	}
	lib.mu.Lock()
	lib.inactive = false
	lib.mu.Unlock()

	// Renamed, with a request; then deleted: the grant is revoked, the
	// other project's is not.
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
	eventually(t, "the deleted project's grant is revoked", func() bool { return slices.Equal(lib.revokedGrants(), []string{"grant:1"}) })
	if _, err := store.Project(ctx, "user:alice", project.ID); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("a deleted project = %v", err)
	}
	if detail, err := store.Project(ctx, "user:alice", other.ID); err != nil || detail.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("the other project = %+v, %v", detail, err)
	}
}

func TestARevocationThePhotoLibraryMissedIsTriedAgain(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "montage")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lib := newLibrary()
	lib.add("asset:a1", "user:alice", "object:1", 2)
	at := &clock{at: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	store := openStore(t, root, lib, at.now)
	project, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1"})
	if err != nil {
		t.Fatal(err)
	}
	lib.setDown(true)
	// Deleting does not wait for the photo library; the revocation it tried
	// at once failed and waits for its next try.
	if err := store.DeleteProject(ctx, "user:alice", project.ID); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.OpenReadOnly(filepath.Join(root, montage.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	eventually(t, "the revocation is tried once", func() bool {
		var attempts int
		return db.QueryRow("SELECT attempts FROM revocations WHERE grant_id = 'grant:1'").Scan(&attempts) == nil && attempts == 1
	})
	lib.setDown(false)
	store.Upkeep(ctx)
	if revoked := lib.revokedGrants(); len(revoked) != 0 {
		t.Fatalf("retried before its time: %v", revoked)
	}
	at.advance(2 * time.Minute)
	store.Upkeep(ctx)
	if revoked := lib.revokedGrants(); !slices.Equal(revoked, []string{"grant:1"}) {
		t.Fatalf("revoked = %v", revoked)
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
	// Two creations the photo library did not answer: one before its
	// registration, one after it, then the service stopped.
	lib.setDown(true)
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:3", "", []string{"asset:a3"}); !errors.Is(err, montage.ErrPhotosUnavailable) {
		t.Fatalf("CreateProject() while the photo library is away = %v", err)
	}
	lib.setDown(false)
	lib.mu.Lock()
	lib.registered = func() { lib.setDown(true) }
	lib.mu.Unlock()
	lib.add("asset:a5", "user:alice", "object:5", 7)
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:4", "", []string{"asset:a5"}); !errors.Is(err, montage.ErrPhotosUnavailable) {
		t.Fatalf("CreateProject() with the photo library gone after registering = %v", err)
	}
	lib.mu.Lock()
	lib.registered = nil
	lib.mu.Unlock()
	lib.setDown(false)
	unfinished, _ := lib.grantOf(projectOf(t, lib, "asset:a5"))
	// A grant no project holds, and the Catalog restored to before the
	// second project's grant.
	if _, err := lib.Register(ctx, "alice", "project:orphan", []string{"asset:a3"}); err != nil {
		t.Fatal(err)
	}
	orphan, _ := lib.grantOf("project:orphan")
	lib.forget(lost.ID)

	_ = store.Close()
	store = openStore(t, root, lib, nil)
	store.Upkeep(ctx)
	if revoked := lib.revokedGrants(); !slices.Equal(revoked, []string{orphan.ID}) {
		t.Fatalf("revoked = %v; want the grant no project holds (%s) alone", revoked, orphan.ID)
	}
	if detail, err := store.Project(ctx, "user:alice", kept.ID); err != nil || detail.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("kept project = %+v, %v", detail, err)
	}
	if detail, err := store.Project(ctx, "user:alice", lost.ID); err != nil || detail.Assets[0].Status != montage.AssetMissing {
		t.Fatalf("project whose grant is gone = %+v, %v", detail, err)
	}
	// The member repeats the first unfinished request: it finishes, with
	// one grant.
	retried, created, err := store.CreateProject(ctx, "user:alice", "alice", "key:3", "", []string{"asset:a3"})
	if err != nil || !created || retried.AssetCount != 1 {
		t.Fatalf("retrying = %+v, %v, %v", retried, created, err)
	}
	// The second, never repeated, is settled by the next round: its project
	// goes and its grant is revoked.
	store.Upkeep(ctx)
	if revoked := lib.revokedGrants(); !slices.Equal(revoked, []string{orphan.ID, unfinished.ID}) {
		t.Fatalf("revoked = %v; want the unfinished creation's grant (%s) too", revoked, unfinished.ID)
	}
	page, err := store.Projects(ctx, "user:alice", "")
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("projects after reconciling = %+v, %v", page, err)
	}
	// Its key, repeated now, makes a project anew.
	if again, created, err := store.CreateProject(ctx, "user:alice", "alice", "key:4", "", []string{"asset:a5"}); err != nil || !created || again.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("repeating the settled request = %+v, %v, %v", again, created, err)
	}
}

// projectOf finds the project the photo library holds a video for.
func projectOf(t *testing.T, lib *library, assetID string) string {
	t.Helper()
	lib.mu.Lock()
	defer lib.mu.Unlock()
	for _, g := range lib.grants {
		if g.assets[assetID] && g.ProjectID != "project:orphan" {
			return g.ProjectID
		}
	}
	t.Fatalf("no grant holds %s", assetID)
	return ""
}

// Reconciling while a registration is in progress waits for it, and leaves
// the project it makes alone.
func TestReconcilingLeavesARegistrationInProgressAlone(t *testing.T) {
	ctx := context.Background()
	store, lib, _ := newStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	lib.mu.Lock()
	lib.registered = func() { close(entered); <-release }
	lib.mu.Unlock()
	type result struct {
		detail montage.ProjectDetail
		err    error
	}
	created := make(chan result, 1)
	go func() {
		detail, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1"})
		created <- result{detail, err}
	}()
	<-entered
	upkept := make(chan struct{})
	go func() {
		defer close(upkept)
		store.Upkeep(ctx)
		store.Upkeep(ctx)
	}()
	select {
	case <-upkept:
		t.Fatal("reconciling went through a registration in progress")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	got := <-created
	<-upkept
	if got.err != nil || got.detail.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("CreateProject() = %+v, %v", got.detail, got.err)
	}
	if revoked := lib.revokedGrants(); len(revoked) != 0 {
		t.Fatalf("revoked = %v", revoked)
	}
	if detail, err := store.Project(ctx, "user:alice", got.detail.ID); err != nil || detail.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("after reconciling: %+v, %v", detail, err)
	}
}

// A request repeated while the first is still registering waits for it and
// gets its project.
func TestARepeatedRequestWaitsForTheFirst(t *testing.T) {
	ctx := context.Background()
	store, lib, _ := newStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	lib.mu.Lock()
	lib.registered = func() { close(entered); <-release }
	lib.mu.Unlock()
	type result struct {
		id      string
		created bool
		err     error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	create := func(out chan<- result) {
		detail, created, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1"})
		out <- result{detail.ID, created, err}
	}
	go create(first)
	<-entered
	lib.mu.Lock()
	lib.registered = nil
	lib.mu.Unlock()
	go create(second)
	time.Sleep(20 * time.Millisecond) // the second request reaches the member's lock
	close(release)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || !a.created || b.created || a.id != b.id || lib.registrations() != 1 {
		t.Fatalf("first = %+v, second = %+v, registrations %d", a, b, lib.registrations())
	}
}

// A project may hold more videos than one lookup takes.
func TestAProjectOfMoreVideosThanOneLookupShowsThemAll(t *testing.T) {
	ctx := context.Background()
	store, lib, _ := newStore(t)
	ids := make([]string, montage.MaxAssets+100)
	for i := range ids {
		ids[i] = fmt.Sprintf("asset:m%03d", i)
		lib.add(ids[i], "user:alice", "object:"+ids[i], 2)
	}
	project, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", ids[:400])
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:2", ids[300:])
	if err != nil || detail.AssetCount != len(ids) {
		t.Fatalf("AddAssets() = %d videos, %v", detail.AssetCount, err)
	}
	for _, asset := range detail.Assets {
		if asset.Status != montage.AssetAvailable {
			t.Fatalf("%s is %s", asset.AssetID, asset.Status)
		}
	}
}

// A registration that fails after the photo library granted takes back
// what it granted: a new project's grant is revoked, an addition's videos
// leave the grant, and the videos the project held stay in it.
func TestAFailedRegistrationTakesBackItsGrant(t *testing.T) {
	ctx := context.Background()
	store, lib, _ := newStore(t)
	// The video goes to the trash between the grant and the lookup.
	lib.mu.Lock()
	lib.registered = func() { lib.trash("asset:a1") }
	lib.mu.Unlock()
	if _, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1"}); !errors.Is(err, montage.ErrAssetsRefused) {
		t.Fatalf("CreateProject() = %v", err)
	}
	eventually(t, "the refused project's grant is revoked", func() bool { return len(lib.revokedGrants()) == 1 })
	if page, _ := store.Projects(ctx, "user:alice", ""); len(page.Items) != 0 {
		t.Fatalf("projects = %+v", page)
	}

	lib.mu.Lock()
	lib.registered = nil
	lib.mu.Unlock()
	project, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:2", "", []string{"asset:a2"})
	if err != nil {
		t.Fatal(err)
	}
	lib.mu.Lock()
	lib.registered = func() { lib.trash("asset:a3") }
	lib.mu.Unlock()
	if _, err := store.AddAssets(ctx, "user:alice", "alice", project.ID, "key:3", []string{"asset:a2", "asset:a3"}); !errors.Is(err, montage.ErrAssetsRefused) {
		t.Fatalf("AddAssets() = %v", err)
	}
	g, _ := lib.grantOf(project.ID)
	if !g.assets["asset:a2"] || g.assets["asset:a3"] || !slices.Equal(lib.removed, []string{"asset:a3"}) {
		t.Fatalf("grant = %v, removed %v; want the project's video kept and the new one out", g.assets, lib.removed)
	}
	if detail, err := store.Project(ctx, "user:alice", project.ID); err != nil || detail.AssetCount != 1 {
		t.Fatalf("project = %+v, %v", detail, err)
	}
}

// A project whose grant the Catalog lost takes the grant an addition
// registers again; one whose grant was revoked takes no more videos.
func TestAProjectWhoseGrantWentAway(t *testing.T) {
	ctx := context.Background()
	store, lib, _ := newStore(t)
	lost, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1"})
	if err != nil {
		t.Fatal(err)
	}
	lib.forget(lost.ID)
	store.Upkeep(ctx)
	detail, err := store.AddAssets(ctx, "user:alice", "alice", lost.ID, "key:2", []string{"asset:a2"})
	if err != nil || !slices.Equal(statuses(detail), []montage.AssetStatus{montage.AssetMissing, montage.AssetAvailable}) {
		t.Fatalf("after the grant was registered again: %v, %v", statuses(detail), err)
	}

	revoked, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:3", "", []string{"asset:a3"})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := lib.grantOf(revoked.ID)
	if err := lib.Revoke(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAssets(ctx, "user:alice", "alice", revoked.ID, "key:4", []string{"asset:a2"}); !errors.Is(err, montage.ErrGrantRevoked) {
		t.Fatalf("AddAssets() to a project whose grant was revoked = %v", err)
	}
	if detail, err := store.Project(ctx, "user:alice", revoked.ID); err != nil || detail.Assets[0].Status != montage.AssetMissing {
		t.Fatalf("project whose grant was revoked = %+v, %v", detail, err)
	}
}

// montage.db restored to before an addition that registered a project's
// grant again: reconciling makes that grant the project's.
func TestReconcilingFindsAGrantRegisteredAgain(t *testing.T) {
	ctx := context.Background()
	store, lib, root := newStore(t)
	project, _, err := store.CreateProject(ctx, "user:alice", "alice", "key:1", "", []string{"asset:a1"})
	if err != nil {
		t.Fatal(err)
	}
	lib.forget(project.ID)
	if _, err := lib.Register(ctx, "alice", project.ID, []string{"asset:a1"}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store = openStore(t, root, lib, nil)
	store.Upkeep(ctx)
	if detail, err := store.Project(ctx, "user:alice", project.ID); err != nil || detail.Assets[0].Status != montage.AssetAvailable {
		t.Fatalf("project = %+v, %v", detail, err)
	}
	if revoked := lib.revokedGrants(); len(revoked) != 0 {
		t.Fatalf("revoked = %v", revoked)
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
