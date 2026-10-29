package systemlog_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/systemlog"
)

func TestParseMessageReadsJSONLogsAndKeepsPlainLines(t *testing.T) {
	cases := []struct {
		name, message string
		priority      int
		level         systemlog.Level
		text          string
		fields        map[string]string
	}{
		{"slog", `{"time":"2026-10-12T08:00:00Z","level":"ERROR","msg":"host state read failed","error":"smartctl: exit status 4","attempt":3}`, 6,
			systemlog.LevelError, "host state read failed", map[string]string{"error": "smartctl: exit status 4", "attempt": "3"}},
		{"slog custom level", `{"level":"WARN+2","msg":"slow"}`, 6, systemlog.LevelWarning, "slow", nil},
		{"caddy", `{"level":"warn","ts":1760256000.1,"logger":"http.auto_https","msg":"automatic HTTPS is completely disabled"}`, 6,
			systemlog.LevelWarning, "automatic HTTPS is completely disabled", map[string]string{"logger": "http.auto_https"}},
		{"systemd failure", "anas-photos.service: Main process exited, code=exited, status=1/FAILURE", 3, systemlog.LevelError,
			"anas-photos.service: Main process exited, code=exited, status=1/FAILURE", nil},
		{"notice", "smbd version 4.22.4 started.", 5, systemlog.LevelInfo, "smbd version 4.22.4 started.", nil},
		{"debug", "trace", 7, systemlog.LevelDebug, "trace", nil},
		{"json without msg", `{"a":1}`, 4, systemlog.LevelWarning, `{"a":1}`, nil},
		{"broken json", `{"msg":`, 6, systemlog.LevelInfo, `{"msg":`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			level, text, fields := systemlog.ParseMessage(tc.message, tc.priority)
			if level != tc.level || text != tc.text || len(fields) != len(tc.fields) {
				t.Fatalf("ParseMessage() = %q, %q, %v", level, text, fields)
			}
			for key, value := range tc.fields {
				if fields[key] != value {
					t.Fatalf("field %s = %q, want %q", key, fields[key], value)
				}
			}
		})
	}
}

func TestServicesCoverTemplatesAndNothingElse(t *testing.T) {
	for unit, want := range map[string]string{
		"anas-api.service": "api", "anas-ffmpeg@3-1418-0.service": "media", "anas-ffmpeg.socket": "media",
		"anas-kiosk@tty1.service": "kiosk", "smbd.service": "samba", "caddy.service": "caddy",
	} {
		if got, ok := systemlog.ServiceOfUnit(unit); !ok || got != want {
			t.Errorf("ServiceOfUnit(%q) = %q, %v", unit, got, ok)
		}
	}
	for _, unit := range []string{"ssh.service", "sudo.service", "docker.service", "user@1000.service", "anas-apix.service"} {
		if _, ok := systemlog.ServiceOfUnit(unit); ok {
			t.Errorf("%s is readable", unit)
		}
	}
}

func TestParseQueryAcceptsOnlyAllowlistedFiltersAndJournalCursors(t *testing.T) {
	q, err := systemlog.ParseQuery(url.Values{"service": {"photos"}, "level": {"warning"}, "q": {"failed"}, "before": {"s=ab12;i=3f"}, "limit": {"50"},
		"since": {"2026-10-12T00:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Services) != 1 || q.Services[0] != "photos" || q.MinLevel != systemlog.LevelWarning || q.Before != "s=ab12;i=3f" || q.Limit != 50 {
		t.Fatalf("ParseQuery() = %+v", q)
	}
	again, err := systemlog.ParseQuery(q.Values())
	if err != nil || again.Before != q.Before || again.Text != q.Text || !again.Since.Equal(q.Since) {
		t.Fatalf("round trip = %+v, %v", again, err)
	}
	for _, bad := range []url.Values{
		{"service": {"ssh"}}, {"service": {"anas-api.service"}}, {"level": {"loud"}}, {"limit": {"501"}},
		{"before": {"s=1;--grep=x"}}, {"after": {"--unit=ssh"}}, {"before": {"s=1"}, "after": {"s=2"}}, {"q": {strings.Repeat("x", 201)}},
	} {
		if _, err := systemlog.ParseQuery(bad); err == nil {
			t.Errorf("ParseQuery(%v) accepted", bad)
		}
	}
}

func TestFakePagesBackwardsAndFollowsNewLines(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	fake := systemlog.NewFake(func() time.Time { return now })
	ctx := context.Background()
	first, err := fake.ReadLogs(ctx, systemlog.Query{Limit: 5})
	if err != nil || len(first.Items) != 5 || first.Next == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, _ := fake.ReadLogs(ctx, systemlog.Query{Limit: 5, Before: first.Next})
	if second.Items[0].Time.After(first.Items[4].Time) || second.Items[0].Cursor == first.Items[4].Cursor {
		t.Fatalf("second page starts with %+v after %+v", second.Items[0], first.Items[4])
	}
	failures, _ := fake.ReadLogs(ctx, systemlog.Query{Limit: 100, MinLevel: systemlog.LevelError, Services: []string{"photos"}})
	if len(failures.Items) == 0 {
		t.Fatal("no photo service errors")
	}
	for _, entry := range failures.Items {
		if entry.Level != systemlog.LevelError || entry.Service != "photos" {
			t.Fatalf("filtered entry = %+v", entry)
		}
	}
	now = now.Add(time.Minute)
	newer, _ := fake.ReadLogs(ctx, systemlog.Query{Limit: 10, After: first.Items[0].Cursor})
	if len(newer.Items) != 2 || !newer.Items[0].Time.After(newer.Items[1].Time) {
		t.Fatalf("lines after a minute = %+v", newer.Items)
	}
}
