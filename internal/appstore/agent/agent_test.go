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
	client := agent.NewClient(serve(t, agent.NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))))
	ctx := context.Background()

	apps, err := client.Apps(ctx)
	if err != nil || len(apps) < 20 {
		t.Fatalf("Apps() = %d apps, %v", len(apps), err)
	}
	icon, contentType, err := client.Icon(ctx, "memos")
	if err != nil || contentType != "image/png" || !bytes.HasPrefix(icon, []byte("\x89PNG")) {
		t.Fatalf("Icon() = %d bytes %q, %v", len(icon), contentType, err)
	}
	plan, err := client.Plan(ctx, "memos")
	if err != nil || len(plan.Digest) != 64 || !strings.Contains(string(plan.Compose), "io.a-nas.app: memos") {
		t.Fatalf("Plan() = %+v, %v", plan, err)
	}

	if _, err := client.Install(ctx, "memos", strings.Repeat("f", 64)); !errors.Is(err, appstore.ErrPlanChanged) {
		t.Fatalf("stale install error = %v", err)
	}
	job, err := client.Install(ctx, "memos", plan.Digest)
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
	if _, err := client.Plan(ctx, "Not Valid"); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("invalid ID error = %v", err)
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
