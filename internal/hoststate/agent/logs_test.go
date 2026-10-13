package agent_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
)

type recordingLogs struct {
	query systemlog.Query
	page  systemlog.Page
}

func (r *recordingLogs) ReadLogs(_ context.Context, q systemlog.Query) (systemlog.Page, error) {
	r.query = q
	return r.page, nil
}

func TestClientReadsSystemLogsWithTheValidatedQuery(t *testing.T) {
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	logs := &recordingLogs{page: systemlog.Page{
		Items: []systemlog.Entry{{Cursor: "s=1;i=2", Time: at, Service: "photos", Unit: "anas-photos.service", Level: systemlog.LevelError,
			Message: "photo reconciliation failed", Fields: map[string]string{"error": "read-only file system"}, PID: 689}},
		Next: "s=1;i=2",
	}}
	socket := serve(t, agent.NewOperationsHandler(agent.Services{SystemLogs: logs}, discardLogger()))
	client := agent.NewClient(socket)
	query := systemlog.Query{Services: []string{"photos"}, MinLevel: systemlog.LevelWarning, Text: "failed", Before: "s=1;i=9", Limit: 50, Since: at.Add(-time.Hour)}
	page, err := client.ReadLogs(context.Background(), query)
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if got := logs.query; len(got.Services) != 1 || got.Services[0] != "photos" || got.MinLevel != query.MinLevel || got.Text != query.Text ||
		got.Before != query.Before || got.Limit != 50 || !got.Since.Equal(query.Since) {
		t.Fatalf("Host Agent received %+v, want %+v", got, query)
	}
	if len(page.Items) != 1 || page.Items[0].Fields["error"] != "read-only file system" || page.Next != "s=1;i=2" || !page.Items[0].Time.Equal(at) {
		t.Fatalf("page = %+v", page)
	}

	// The Host Agent validates on its own; a unit outside the allowlist is refused.
	raw := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	response, err := raw.Get("http://host-agent/v1/system-logs?service=ssh")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status for ssh = %d, want 400", response.StatusCode)
	}

	unavailable := agent.NewClient(serve(t, agent.NewOperationsHandler(agent.Services{}, discardLogger())))
	if _, err := unavailable.ReadLogs(context.Background(), systemlog.Query{Limit: 10}); !errors.Is(err, agent.ErrUnavailable) {
		t.Fatalf("ReadLogs() without a reader error = %v", err)
	}
}
