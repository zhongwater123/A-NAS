package engine

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// Pipes hand Compose's output over in chunks that cut lines anywhere.
func writeInChunks(t *testing.T, w interface{ Write([]byte) (int, error) }, data string, size int) {
	t.Helper()
	for len(data) > 0 {
		chunk := data[:min(size, len(data))]
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
		data = data[len(chunk):]
	}
}

func TestLineWriterJoinsLinesCutAcrossWrites(t *testing.T) {
	for _, size := range []int{1, 2, 3, 7, 1000} {
		var lines []string
		w := &lineWriter{each: func(line string) { lines = append(lines, line) }}
		writeInChunks(t, w, "first\nsec\r\n\nthird line\nlast", size)
		w.Flush()
		if want := []string{"first", "sec", "", "third line", "last"}; !slices.Equal(lines, want) {
			t.Errorf("chunks of %d: lines = %q, want %q", size, lines, want)
		}
	}
}

// Output that never ends a line is passed on in pieces instead of growing.
func TestLineWriterBoundsALineWithoutABreak(t *testing.T) {
	var lines []string
	w := &lineWriter{each: func(line string) { lines = append(lines, line) }}
	writeInChunks(t, w, strings.Repeat("x", maxLine+10), 4096)
	w.Flush()
	if len(lines) != 2 || len(lines[0]) < maxLine || len(lines[0])+len(lines[1]) != maxLine+10 {
		t.Fatalf("got %d pieces", len(lines))
	}
}

// A failed pull as Compose 2.40.3 reported it with --progress json (2026-10-28,
// WSL2): progress events repeat, the service's error event and the closing
// error carry Docker's words.
const composeJSONFailure = `{"id":"app","text":"Pulling"}
{"id":"7b229517ed22","parent_id":"app","text":"Pulling fs layer"}
{"id":"7b229517ed22","parent_id":"app","text":"Downloading","status":"[=====>   ]  1.049MB/3.853MB","current":1048576,"total":3853109,"percent":27}
{"id":"7b229517ed22","parent_id":"app","text":"Downloading","status":"[=====>   ]  1.049MB/3.853MB","current":1048576,"total":3853109,"percent":27}
{"id":"7b229517ed22","parent_id":"app","text":"Downloading","status":"[=====>   ]  1.049MB/3.853MB","current":1048576,"total":3853109,"percent":27}
{"id":"app","text":"Error","status":"unknown: failed to resolve reference \"ghcr.io/x/app:1\": unexpected status from HEAD request to https://ghcr.io/v2/x/app/manifests/1: 429 Too Many Requests"}
{"error":true,"message":"Error response from daemon: unknown: failed to resolve reference \"ghcr.io/x/app:1\": unexpected status from HEAD request to https://ghcr.io/v2/x/app/manifests/1: 429 Too Many Requests"}
`

func TestPullWatchReadsComposeJSONProgress(t *testing.T) {
	for _, size := range []int{1, 5, 64, len(composeJSONFailure)} {
		var output bytes.Buffer
		watch := newPullWatch(&output)
		writeInChunks(t, watch, composeJSONFailure, size)
		watch.Flush()

		lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
		want := []string{
			"app Pulling",
			"7b229517ed22 Pulling fs layer",
			"7b229517ed22 Downloading [=====> ] 1.049MB/3.853MB",
			`app Error unknown: failed to resolve reference "ghcr.io/x/app:1": unexpected status from HEAD request to https://ghcr.io/v2/x/app/manifests/1: 429 Too Many Requests`,
			`Error response from daemon: unknown: failed to resolve reference "ghcr.io/x/app:1": unexpected status from HEAD request to https://ghcr.io/v2/x/app/manifests/1: 429 Too Many Requests`,
		}
		if !slices.Equal(lines, want) {
			t.Errorf("chunks of %d: output =\n%s", size, output.String())
		}
		if problem := watch.problem; problem.retry != "镜像仓库限流" || problem.permanent || problem.reason != want[4] {
			t.Errorf("chunks of %d: problem = %+v", size, problem)
		}
	}
}

// Compose before the closing JSON error prints Docker's error as plain text,
// and a progress line naming an error is not one.
func TestPullWatchReadsPlainErrorsAndNotProgress(t *testing.T) {
	var output bytes.Buffer
	watch := newPullWatch(&output)
	writeInChunks(t, watch, `{"id":"error-pages","text":"Pulling"}`+"\n"+
		`{"id":"3b47e31dda85","parent_id":"error-pages","text":"Downloading","status":"0B"}`+"\n"+
		"toomanyrequests: retry-after: 2s, allowed: 44000/minute", 9)
	watch.Flush()
	problem := watch.problem
	if problem.retry != "镜像仓库限流" || problem.retryAfter.Seconds() != 2 || problem.reason != "toomanyrequests: retry-after: 2s, allowed: 44000/minute" {
		t.Fatalf("problem = %+v", problem)
	}
	if !strings.HasSuffix(output.String(), "3b47e31dda85 Downloading 0B\ntoomanyrequests: retry-after: 2s, allowed: 44000/minute\n") {
		t.Fatalf("output =\n%s", output.String())
	}
}
