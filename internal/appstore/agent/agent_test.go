package agent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/agent"
	"github.com/zhongwater123/A-NAS/internal/appstore/fake"
)

func TestClientRoundTripsTheAppCenter(t *testing.T) {
	store, err := fake.New(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	client := agent.NewClient(serve(t, agent.NewHandler(store, testIdentities, discard)))
	ctx := context.Background()

	apps, err := client.Apps(ctx)
	if err != nil || len(apps) < 20 {
		t.Fatalf("Apps() = %d apps, %v", len(apps), err)
	}
	for _, app := range apps {
		if app.ID == "openlist" && (app.Category != appstore.CategoryFiles || !strings.Contains(app.Notes, "admin") || app.Repo == "" || app.Updated == "") {
			t.Fatalf("openlist = %+v; want its category, first-use notes and links", app.App)
		}
	}
	icon, contentType, err := client.Icon(ctx, "memos")
	if err != nil || contentType != "image/png" || !bytes.HasPrefix(icon, []byte("\x89PNG")) {
		t.Fatalf("Icon() = %d bytes %q, %v", len(icon), contentType, err)
	}
	plan, err := client.Plan(ctx, "memos", testIdentity)
	if err != nil || len(plan.Digest) != 64 || !strings.Contains(string(plan.Compose), "io.a-nas.app: memos") {
		t.Fatalf("Plan() = %+v, %v", plan, err)
	}

	if _, err := client.Install(ctx, "memos", strings.Repeat("f", 64), testIdentity); !errors.Is(err, appstore.ErrPlanChanged) {
		t.Fatalf("stale install error = %v", err)
	}
	job, err := client.Install(ctx, "memos", plan.Digest, testIdentity)
	if err != nil || job.State != appstore.JobRunning || job.Action != appstore.JobInstall {
		t.Fatalf("Install() = %+v, %v", job, err)
	}
	store.Wait()
	apps, _ = client.Apps(ctx)
	for _, app := range apps {
		if app.ID == "memos" && (app.State != appstore.StateInstalled || app.Job.State != appstore.JobSucceeded) {
			t.Fatalf("memos after install = %+v", app)
		}
	}

	if _, err := client.Uninstall(ctx, "navidrome"); !errors.Is(err, appstore.ErrNotInstalled) {
		t.Fatalf("uninstall missing error = %v", err)
	}
	if _, err := client.Plan(ctx, "Not Valid", testIdentity); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("invalid ID error = %v", err)
	}
}

// The agent is installed apart from the product service; on 2026-10-09 an
// agent that added plan fields broke every plan in an older product service.
func TestClientIgnoresFieldsANewerAgentAdds(t *testing.T) {
	client := agent.NewClient(serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apps":[{"id":"memos","title":"Memos","state":"available","running":0,"total":0,"addedLater":true}],"addedLater":1}`)
	})))
	apps, err := client.Apps(context.Background())
	if err != nil || len(apps) != 1 || apps[0].ID != "memos" {
		t.Fatalf("Apps() = %+v, %v; want the known fields and no error", apps, err)
	}
}

// An agent from before A-NAS categories reports the CasaOS ones, which the
// API contract does not allow.
func TestClientDropsCategoriesAnOlderAgentReports(t *testing.T) {
	client := agent.NewClient(serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apps":[{"id":"memos","title":"Memos","category":"Productivity","state":"available","running":0,"total":0},{"id":"navidrome","title":"Navidrome","category":"media","state":"available","running":0,"total":0}]}`)
	})))
	apps, err := client.Apps(context.Background())
	if err != nil || len(apps) != 2 || apps[0].Category != "" || apps[1].Category != appstore.CategoryMedia {
		t.Fatalf("Apps() = %+v, %v; want the CasaOS category dropped and the A-NAS one kept", apps, err)
	}
}

func serve(t *testing.T, handler http.Handler) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

var testIdentity = appstore.Identity{Username: "app-memos", UID: 30002, GID: 30002}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// testIdentities is the host's account database: memos has its account,
// another account holds 30003, navidrome's group alone was made (a creation
// cut short) and another group holds 30005.
var testIdentities = fakeIdentities{
	accounts: map[string]appstore.Identity{"memos": testIdentity},
	users:    map[int]bool{30002: true, 30003: true},
	groups:   map[int]string{30002: "app-memos", 30003: "other", 30004: "app-navidrome", 30005: "app-other"},
}

type fakeIdentities struct {
	accounts map[string]appstore.Identity
	users    map[int]bool
	groups   map[int]string
}

func (f fakeIdentities) App(id string) (appstore.Identity, bool, error) {
	identity, ok := f.accounts[id]
	return identity, ok, nil
}

func (f fakeIdentities) Unused(id string, uid int) (bool, error) {
	group := f.groups[uid]
	return !f.users[uid] && (group == "" || group == appstore.IdentityName(id)), nil
}

// recordingStore records the identities plans and installs run as.
type recordingStore struct {
	appstore.Store
	mu        sync.Mutex
	installed []appstore.Identity
}

func (s *recordingStore) Install(ctx context.Context, id, digest string, identity appstore.Identity) (appstore.Job, error) {
	s.mu.Lock()
	s.installed = append(s.installed, identity)
	s.mu.Unlock()
	return s.Store.Install(ctx, id, digest, identity)
}

// The container agent runs an app as the account the Host Agent made for
// it, whatever identity the caller names (issue #176).
func TestAgentRunsAppsAsTheirAccounts(t *testing.T) {
	store, err := fake.New(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	recording := &recordingStore{Store: store}
	client := agent.NewClient(serve(t, agent.NewHandler(recording, testIdentities, discard)))
	ctx := context.Background()
	other := appstore.Identity{Username: "app-memos", UID: 30003, GID: 30003}

	plan, err := client.Plan(ctx, "memos", other)
	if err != nil || plan.Identity != testIdentity {
		t.Fatalf("Plan(memos) = %+v, %v; want it as app-memos", plan.Identity, err)
	}
	if _, err := client.Install(ctx, "memos", plan.Digest, other); err != nil {
		t.Fatalf("Install(memos) error = %v", err)
	}
	store.Wait()

	// Before its first install an app has no account yet: the plan shows the
	// UID the Host Agent reserved, which no account and no other app's group
	// may hold (navidrome's own group, left by a creation cut short, may),
	// and the app cannot be installed until the account exists.
	reserved := appstore.Identity{Username: "app-navidrome", UID: 30004, GID: 30004}
	plan, err = client.Plan(ctx, "navidrome", reserved)
	if err != nil || plan.Identity != reserved {
		t.Fatalf("Plan(navidrome) = %+v, %v; want the reserved identity", plan.Identity, err)
	}
	for _, identity := range []appstore.Identity{
		{UID: 30003, GID: 30003}, {UID: 30002, GID: 30002}, {UID: 30005, GID: 30005}, {UID: 0, GID: 0}, {UID: 20100, GID: 20100},
		{UID: 30004, GID: 30005},
	} {
		if _, err := client.Plan(ctx, "navidrome", identity); err == nil || !strings.Contains(err.Error(), "invalid_request") {
			t.Errorf("Plan(navidrome, %d:%d) error = %v, want invalid_request", identity.UID, identity.GID, err)
		}
	}
	if _, err := client.Install(ctx, "navidrome", plan.Digest, reserved); !errors.Is(err, appstore.ErrUnavailable) {
		t.Fatalf("Install(navidrome) without its account error = %v, want ErrUnavailable", err)
	}

	recording.mu.Lock()
	defer recording.mu.Unlock()
	if len(recording.installed) != 1 || recording.installed[0] != testIdentity {
		t.Fatalf("installs ran as %+v, want only app-memos", recording.installed)
	}
}

func TestSystemIdentitiesReadTheHostsAccounts(t *testing.T) {
	identities := agent.SystemIdentities{}
	if identity, ok, err := identities.App("no-such-app-176"); ok || err != nil {
		t.Fatalf("App(missing) = %+v, %v, %v", identity, ok, err)
	}
	if unused, err := identities.Unused("no-such-app-176", 0); unused || err != nil {
		t.Fatalf("Unused(0) = %v, %v; root holds it", unused, err)
	}
}
