package linux

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/datavolume"
)

const testMount = "/srv/a-nas/data"

// stateHost is a system root with the legacy and the current state
// directories of the Host Agent.
type stateHost struct {
	systemRoot string
}

func newStateHost(t *testing.T) stateHost {
	t.Helper()
	host := stateHost{systemRoot: t.TempDir()}
	if err := os.MkdirAll(host.legacy(""), 0o700); err != nil {
		t.Fatal(err)
	}
	return host
}

func (h stateHost) legacy(name string) string {
	return filepath.Join(h.systemRoot, "var", "lib", "a-nas", name)
}

func (h stateHost) current(name string) string {
	return filepath.Join(h.systemRoot, "var", "lib", "a-nas-host", name)
}

func (h stateHost) executor() *Executor {
	return NewExecutor(nil, nil, Options{
		SystemRoot: h.systemRoot, MountPoint: testMount, Now: func() time.Time { return testNow },
	})
}

var testNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

// validState is one valid record of each kind, as a release before issue
// #176 left them.
var validState = map[string]string{
	"space-registry.json":    `{"space:alice":"/srv/a-nas/data/spaces/private/alice","space:shared":"/srv/a-nas/data/spaces/shared"}`,
	"identity-registry.json": `{"alice":{"uid":20100,"role":"member","enabled":true},"owner":{"uid":20101,"role":"admin","enabled":true}}`,
	"viewing-grants.json":    `{"viewing:1":{"spaceId":"space:alice","admin":"owner","uid":20101,"expiresAt":"2026-10-24T00:00:00Z"}}`,
	"app-identities.json":    `{"jellyfin":30000,"immich":30001}`,
	"volume-operations.json": `{"operations":[{"planId":"plan:1","kind":"create_volume","disks":[{"diskId":"disk:a","fingerprint":"f"}],` +
		`"layout":"single","formatted":["09e275fe-794a-458d-8200-b6e67c55cc22"],"state":"failed","step":"mount",` +
		`"startedAt":"2026-10-22T00:00:00Z","updatedAt":"2026-10-22T00:00:00Z"}]}`,
}

func checkLoaded(t *testing.T, executor *Executor) {
	t.Helper()
	for name, err := range map[string]error{
		"space registry": executor.registryError, "identities": executor.identityError, "viewing": executor.viewingError,
		"apps": executor.appsError, "operations": executor.operationsError,
	} {
		if err != nil {
			t.Errorf("%s error = %v", name, err)
		}
	}
	if executor.spaceRoots["space:alice"] != "/srv/a-nas/data/spaces/private/alice" || !executor.identityKnown("owner") ||
		executor.viewing["viewing:1"].Admin != "owner" || executor.apps["immich"] != 30001 || executor.operations["plan:1"] == nil {
		t.Errorf("records not loaded: spaces %v, identities %v, viewing %v, apps %v, operations %v",
			executor.spaceRoots, executor.identities, executor.viewing, executor.apps, executor.operations)
	}
}

func TestStateMovesOutOfTheProductServiceDirectoryOnce(t *testing.T) {
	host := newStateHost(t)
	for name, contents := range validState {
		writeFile(t, host.legacy(name), contents)
	}
	checkLoaded(t, host.executor())

	info, err := os.Stat(host.current(""))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state directory = %v, %v; want mode 0700", info, err)
	}
	for name, contents := range validState {
		if got := readFile(t, host.current(name)); got != contents {
			t.Errorf("moved %s = %q, want %q", name, got, contents)
		}
		if info, err := os.Stat(host.current(name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("moved %s = %v, %v; want mode 0600", name, info, err)
		}
		if _, err := os.Lstat(host.legacy(name)); !os.IsNotExist(err) {
			t.Errorf("legacy %s still there: %v", name, err)
		}
	}
	// Starting again moves nothing and reads the same records.
	checkLoaded(t, host.executor())
}

func TestStateResolvesWhatAnInterruptedMoveLeft(t *testing.T) {
	host := newStateHost(t)
	for name, contents := range validState {
		writeFile(t, host.current(name), contents)
	}
	// The move copied this one but stopped before removing the original.
	writeFile(t, host.legacy("app-identities.json"), validState["app-identities.json"])
	// An older release wrote this one after the move.
	writeFile(t, host.legacy("space-registry.json"), `{"space:shared":"/srv/a-nas/data/spaces/shared"}`)

	checkLoaded(t, host.executor())
	if _, err := os.Lstat(host.legacy("app-identities.json")); !os.IsNotExist(err) {
		t.Errorf("the copy of a moved record is still there: %v", err)
	}
	if got := readFile(t, host.legacy("space-registry.json")); !strings.Contains(got, "space:shared") {
		t.Errorf("a differing legacy record was changed: %q", got)
	}
}

func TestStateRefusesRecordsThatAreNotPlainFilesOfTheHostAgent(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, host stateHost)
	}{
		{"legacy symbolic link", func(t *testing.T, host stateHost) {
			target := filepath.Join(t.TempDir(), "elsewhere.json")
			writeFile(t, target, validState["space-registry.json"])
			if err := os.Symlink(target, host.legacy("space-registry.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"current symbolic link", func(t *testing.T, host stateHost) {
			target := filepath.Join(t.TempDir(), "elsewhere.json")
			writeFile(t, target, validState["space-registry.json"])
			if err := os.MkdirAll(host.current(""), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, host.current("space-registry.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"legacy hard link", func(t *testing.T, host stateHost) {
			target := filepath.Join(host.systemRoot, "elsewhere.json")
			writeFile(t, target, validState["space-registry.json"])
			if err := os.Link(target, host.legacy("space-registry.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"legacy FIFO", func(t *testing.T, host stateHost) {
			if err := syscall.Mkfifo(host.legacy("space-registry.json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"legacy directory", func(t *testing.T, host stateHost) {
			if err := os.Mkdir(host.legacy("space-registry.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newStateHost(t)
			test.prepare(t, host)
			done := make(chan *Executor, 1)
			go func() { done <- host.executor() }()
			var executor *Executor
			select {
			case executor = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("NewExecutor() blocked on the record")
			}
			if executor.registryError == nil || len(executor.spaceRoots) != 0 {
				t.Fatalf("space registry = %v, error %v; want it refused", executor.spaceRoots, executor.registryError)
			}
			if executor.identityError != nil || executor.appsError != nil {
				t.Errorf("other registries refused too: %v, %v", executor.identityError, executor.appsError)
			}
		})
	}
}

func TestStateFilesMustBelongToTheHostAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space-registry.json")
	writeFile(t, path, "{}")
	if _, err := readStateFile(path, os.Geteuid()); err != nil {
		t.Fatalf("readStateFile(own file) error = %v", err)
	}
	if _, err := readStateFile(path, os.Geteuid()+1); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("readStateFile(another owner's file) error = %v, want refused", err)
	}
}

func TestStateDirectoryIsForRootAlone(t *testing.T) {
	host := newStateHost(t)
	if err := os.MkdirAll(host.current(""), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(host.current(""), 0o755); err != nil {
		t.Fatal(err)
	}
	if executor := host.executor(); executor.registryError != nil {
		t.Fatalf("registry error = %v", executor.registryError)
	}
	if info, err := os.Stat(host.current("")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state directory = %v, %v; want it tightened to 0700", info, err)
	}

	linked := newStateHost(t)
	if err := os.Symlink(t.TempDir(), linked.current("")); err != nil {
		t.Fatal(err)
	}
	executor := linked.executor()
	for name, err := range map[string]error{
		"space registry": executor.registryError, "identities": executor.identityError, "viewing": executor.viewingError,
		"apps": executor.appsError, "operations": executor.operationsError,
	} {
		if err == nil {
			t.Errorf("%s loaded from a symbolic link to a directory", name)
		}
	}
}

// Every record is checked on load with the rules its requests are checked
// with; a record that fails them is not used.
func TestStateRecordsAreCheckedLikeTheirRequests(t *testing.T) {
	operation := func(change func(map[string]any)) string {
		entry := map[string]any{
			"planId": "plan:2", "kind": "add_disks", "disks": []map[string]string{{"diskId": "disk:b", "fingerprint": "f"}},
			"layout": "mirror", "filesystemUuid": "09e275fe-794a-458d-8200-b6e67c55cc22", "state": "interrupted", "step": "erase",
		}
		change(entry)
		document, err := json.Marshal(map[string]any{"operations": []any{entry}})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	for _, test := range []struct {
		name, file, contents string
		failed               func(*Executor) (int, error)
	}{
		{"the mount point as a space", "space-registry.json", `{"space:x":"/srv/a-nas/data"}`, registryError},
		{"the photo store as a space", "space-registry.json", `{"space:x":"/srv/a-nas/data/photos"}`, registryError},
		{"app data as a space", "space-registry.json", `{"space:x":"/srv/a-nas/data/apps/immich"}`, registryError},
		{"a space outside the volume", "space-registry.json", `{"space:x":"/etc"}`, registryError},
		{"an unclean space root", "space-registry.json", `{"space:x":"/srv/a-nas/data/spaces/private/../shared"}`, registryError},
		{"Shared elsewhere", "space-registry.json", `{"space:shared":"/srv/a-nas/data/spaces/private/alice"}`, registryError},
		{"a private space without an owner name", "space-registry.json", `{"space:x":"/srv/a-nas/data/spaces/private/A"}`, registryError},
		{"two accounts with one UID", "identity-registry.json",
			`{"alice":{"uid":20100,"role":"member"},"bob":{"uid":20100,"role":"member"}}`, identityError},
		{"an account outside the range", "identity-registry.json", `{"root":{"uid":0,"role":"admin"}}`, identityError},
		{"a grant to a UID outside the range", "viewing-grants.json",
			`{"viewing:1":{"spaceId":"space:alice","admin":"owner","uid":0,"expiresAt":"2026-10-24T00:00:00Z"}}`, viewingError},
		{"a grant on Shared", "viewing-grants.json",
			`{"viewing:1":{"spaceId":"space:shared","admin":"owner","uid":20101,"expiresAt":"2026-10-24T00:00:00Z"}}`, viewingError},
		{"two apps with one UID", "app-identities.json", `{"jellyfin":30000,"immich":30000}`, appsError},
		{"an app outside the range", "app-identities.json", `{"jellyfin":20100}`, appsError},
		{"an app ID too long for an account name", "app-identities.json", `{"` + strings.Repeat("a", 40) + `":30000}`, appsError},
		{"an unknown kind", "volume-operations.json", operation(func(o map[string]any) { o["kind"] = "format_everything" }), operationsError},
		{"an addition without a volume", "volume-operations.json", operation(func(o map[string]any) { delete(o, "filesystemUuid") }), operationsError},
		{"an addition that formatted", "volume-operations.json",
			operation(func(o map[string]any) { o["formatted"] = []string{"09e275fe-794a-458d-8200-b6e67c55cc22"} }), operationsError},
		{"a creation into a volume", "volume-operations.json", operation(func(o map[string]any) {
			o["kind"], o["layout"] = "create_volume", "single"
		}), operationsError},
		{"a creation that formatted something else", "volume-operations.json", operation(func(o map[string]any) {
			o["kind"], o["layout"], o["formatted"] = "create_volume", "single", []string{"/dev/sda"}
			delete(o, "filesystemUuid")
		}), operationsError},
		{"a step of the other kind", "volume-operations.json", operation(func(o map[string]any) { o["step"] = "format" }), operationsError},
		{"a layout the disks do not fit", "volume-operations.json", operation(func(o map[string]any) { o["layout"] = "single" }), operationsError},
		{"no disks", "volume-operations.json", operation(func(o map[string]any) { o["disks"] = []any{} }), operationsError},
		{"an unknown state", "volume-operations.json", operation(func(o map[string]any) { o["state"] = "done" }), operationsError},
		{"success without a volume", "volume-operations.json", operation(func(o map[string]any) {
			o["state"] = "succeeded"
			delete(o, "step")
		}), operationsError},
		{"a volume without success", "volume-operations.json",
			operation(func(o map[string]any) { o["volume"] = map[string]any{"id": "volume:data", "layout": "mirror"} }), operationsError},
		{"one plan twice", "volume-operations.json",
			`{"operations":[{"planId":"plan:1","kind":"create_volume","disks":[{"diskId":"disk:a"}],"layout":"single","state":"failed"},` +
				`{"planId":"plan:1","kind":"create_volume","disks":[{"diskId":"disk:a"}],"layout":"single","state":"failed"}]}`, operationsError},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newStateHost(t)
			for name, contents := range validState {
				writeFile(t, host.current(name), contents)
			}
			writeFile(t, host.current(test.file), test.contents)
			executor := host.executor()
			if loaded, err := test.failed(executor); err == nil || loaded != 0 {
				t.Fatalf("%s: error %v, %d records used; want it refused whole", test.contents, err, loaded)
			}
		})
	}
	// The valid journal entry of the add-disks kind loads.
	host := newStateHost(t)
	writeFile(t, host.current("volume-operations.json"), operation(func(map[string]any) {}))
	if executor := host.executor(); executor.operationsError != nil || executor.operations["plan:2"].State != datavolume.OperationInterrupted {
		t.Fatalf("valid journal = %v, %v", executor.operations, executor.operationsError)
	}
}

func registryError(e *Executor) (int, error)   { return len(e.spaceRoots), e.registryError }
func identityError(e *Executor) (int, error)   { return len(e.identities), e.identityError }
func viewingError(e *Executor) (int, error)    { return len(e.viewing), e.viewingError }
func appsError(e *Executor) (int, error)       { return len(e.apps), e.appsError }
func operationsError(e *Executor) (int, error) { return len(e.operations), e.operationsError }

// A grant that GrantViewing would refuse now loads as expired, so the next
// expiry pass revokes it rather than keeping its entries.
func TestStateViewingGrantsOutsideTheRulesLoadExpired(t *testing.T) {
	host := newStateHost(t)
	writeFile(t, host.current("identity-registry.json"),
		`{"alice":{"uid":20100,"role":"member","enabled":true},"owner":{"uid":20101,"role":"admin","enabled":true},`+
			`"former":{"uid":20102,"role":"member","enabled":true},"gone":{"uid":20103,"role":"admin","enabled":false}}`)
	grant := func(admin string, uid int, expires time.Time) map[string]any {
		return map[string]any{"spaceId": "space:alice", "admin": admin, "uid": uid, "expiresAt": expires}
	}
	grants, err := json.Marshal(map[string]any{
		"viewing:valid":    grant("owner", 20101, testNow.Add(accountid.ViewingDuration)),
		"viewing:past":     grant("owner", 20101, testNow.Add(-time.Hour)),
		"viewing:forever":  grant("owner", 20101, testNow.AddDate(10, 0, 0)),
		"viewing:demoted":  grant("former", 20102, testNow.Add(time.Hour)),
		"viewing:disabled": grant("gone", 20103, testNow.Add(time.Hour)),
		"viewing:uid":      grant("owner", 20102, testNow.Add(time.Hour)),
		"viewing:unknown":  grant("nobody", 20104, testNow.Add(time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, host.current("viewing-grants.json"), string(grants))
	executor := host.executor()
	if executor.viewingError != nil {
		t.Fatal(executor.viewingError)
	}
	for id, want := range map[string]time.Time{
		"viewing:valid": testNow.Add(accountid.ViewingDuration), "viewing:past": testNow.Add(-time.Hour),
		"viewing:forever": {}, "viewing:demoted": {}, "viewing:disabled": {}, "viewing:uid": {}, "viewing:unknown": {},
	} {
		if got := executor.viewing[id].ExpiresAt; !got.Equal(want) {
			t.Errorf("%s expires at %v, want %v", id, got, want)
		}
	}
	if viewers := executor.viewersOf("space:alice"); len(viewers) != 1 || viewers[0] != "owner" {
		t.Errorf("viewers = %v, want only the valid grant's administrator", viewers)
	}
}
