package journal_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/systemlog"
	"github.com/zhongwater123/A-NAS/internal/systemlog/journal"
)

func line(cursor string, microseconds int64, unit, priority, message string) string {
	return fmt.Sprintf(`{"__CURSOR":%q,"__REALTIME_TIMESTAMP":"%d","_SYSTEMD_UNIT":%q,"PRIORITY":%q,"_PID":"671","MESSAGE":%s}`,
		cursor, microseconds, unit, priority, message)
}

type recorder struct {
	args   [][]string
	output string
	stderr string
	err    error
}

func (r *recorder) open(_ context.Context, args []string) (journal.Stream, error) {
	r.args = append(r.args, args)
	return journal.Stream{Output: strings.NewReader(r.output), Finish: func(bool) (string, error) { return r.stderr, r.err }}, nil
}

func TestArgumentsUseOnlyAllowlistedUnitsAndValidatedValues(t *testing.T) {
	args := strings.Join(journal.Arguments(systemlog.Query{Services: []string{"media"}, Before: "s=ab;i=1f",
		Since: time.Unix(1760227200, 0)}), " ")
	for _, want := range []string{"--output=json", "--unit=anas-media@*.service", "--unit=anas-media.socket", "--reverse", "--after-cursor=s=ab;i=1f", "--since=@1760227200"} {
		if !strings.Contains(args, want) {
			t.Fatalf("arguments %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "anas-api") || strings.Contains(args, "--grep") {
		t.Fatalf("arguments %q read more than asked", args)
	}
	all := strings.Join(journal.Arguments(systemlog.Query{Text: "x; rm -rf /"}), " ")
	if strings.Count(all, "--unit=") != len(systemlog.Units(nil)) || strings.Contains(all, "rm -rf") {
		t.Fatalf("arguments for all services = %q", all)
	}
	follow := strings.Join(journal.Arguments(systemlog.Query{After: "s=ab;i=1f"}), " ")
	if strings.Contains(follow, "--reverse") || !strings.Contains(follow, "--after-cursor=s=ab;i=1f") {
		t.Fatalf("follow arguments = %q", follow)
	}
}

func TestReadLogsParsesFiltersAndPagesTheJournal(t *testing.T) {
	output := strings.Join([]string{
		line("s=1;i=5", 1760256005000000, "anas-api.service", "6", `"{\"level\":\"ERROR\",\"msg\":\"host state read failed\",\"error\":\"timeout\"}"`),
		line("s=1;i=4", 1760256004000000, "anas-api.service", "6", `"{\"level\":\"INFO\",\"msg\":\"A-NAS API listening\"}"`),
		`{"__CURSOR":"s=1;i=3","__REALTIME_TIMESTAMP":"1760256003000000","UNIT":"anas-photos.service","_PID":"1","PRIORITY":"3","MESSAGE":"anas-photos.service: Failed with result 'exit-code'."}`,
		line("s=1;i=2", 1760256002000000, "ssh.service", "6", `"Accepted publickey"`),
		line("s=1;i=1", 1760256001000000, "smbd.service", "4", `[104,105,255]`),
		"not json",
	}, "\n") + "\n"
	r := &recorder{output: output}
	reader := journal.NewWithOpener(r.open, time.Second)
	ctx := context.Background()

	page, err := reader.ReadLogs(ctx, systemlog.Query{Limit: 10, MinLevel: systemlog.LevelWarning})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range page.Items {
		got = append(got, entry.Service+"|"+string(entry.Level)+"|"+entry.Message)
	}
	want := []string{
		"api|error|host state read failed",
		"photos|error|anas-photos.service: Failed with result 'exit-code'.",
		"samba|warning|hi�",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if page.Items[0].Fields["error"] != "timeout" || page.Items[0].PID != 671 || page.Next != "" {
		t.Fatalf("first entry = %+v, next = %q", page.Items[0], page.Next)
	}

	full, _ := reader.ReadLogs(ctx, systemlog.Query{Limit: 2})
	if len(full.Items) != 2 || full.Next != "s=1;i=4" || full.ScannedThrough != nil {
		t.Fatalf("full page = %+v", full)
	}
	follow, _ := reader.ReadLogs(ctx, systemlog.Query{Limit: 2, After: "s=1;i=0"})
	if !follow.Overflow || follow.Items[0].Cursor != "s=1;i=4" {
		t.Fatalf("follow page = %+v", follow)
	}
}

func TestReadLogsStopsAtTheScanLimitWithACursorToContinue(t *testing.T) {
	var lines []string
	for index := systemlog.ScanLimit + 10; index > 0; index-- {
		lines = append(lines, line(fmt.Sprintf("s=1;i=%x", index), int64(1760256000000000+index), "anas-api.service", "6", `"routine"`))
	}
	reader := journal.NewWithOpener((&recorder{output: strings.Join(lines, "\n")}).open, time.Second)
	page, err := reader.ReadLogs(context.Background(), systemlog.Query{Limit: 10, Text: "never"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Next != fmt.Sprintf("s=1;i=%x", 11) || page.ScannedThrough == nil {
		t.Fatalf("page = %+v", page)
	}
}

func TestReadLogsTreatsUnitGlobsWithoutEntriesAsEmpty(t *testing.T) {
	ctx := context.Background()
	empty := &recorder{stderr: "Failed to add filter for units: No data available\n", err: errors.New("exit status 1")}
	page, err := journal.NewWithOpener(empty.open, time.Second).ReadLogs(ctx, systemlog.Query{Limit: 5, Services: []string{"kiosk"}})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("kiosk without entries = %+v, %v", page, err)
	}
	broken := &recorder{stderr: "Failed to seek to cursor: Invalid argument\n", err: errors.New("exit status 1")}
	if _, err := journal.NewWithOpener(broken.open, time.Second).ReadLogs(ctx, systemlog.Query{Limit: 5, Before: "s=1;i=1"}); err == nil {
		t.Fatal("a failed journalctl looks like an empty log")
	}
}
