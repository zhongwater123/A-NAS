// Package journal reads the allowlisted A-NAS service logs from the systemd
// journal for the Host Agent (ADR 0020). It runs journalctl with fixed
// arguments built from a validated systemlog.Query, never through a shell,
// and filters levels and text itself instead of passing expressions on.
package journal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/systemlog"
)

// Timeout bounds one read; what was read by then is returned.
const Timeout = 10 * time.Second

// maxLine bounds one JSON line; journald entries are far smaller in practice.
const maxLine = 4 << 20

// Stream is journalctl's output. Finish waits for the process to end, first
// stopping it when the read has what it needs, and reports how it ended.
type Stream struct {
	Output io.Reader
	Finish func(stop bool) (stderr string, err error)
}

// Opener starts journalctl with args.
type Opener func(ctx context.Context, args []string) (Stream, error)

type Reader struct {
	open    Opener
	timeout time.Duration
}

// New reads the local journal with journalctl.
func New() *Reader { return &Reader{open: runJournalctl, timeout: Timeout} }

// NewWithOpener lets tests replace journalctl.
func NewWithOpener(open Opener, timeout time.Duration) *Reader {
	return &Reader{open: open, timeout: timeout}
}

// Arguments returns the journalctl arguments for q.
func Arguments(q systemlog.Query) []string {
	args := []string{
		"--output=json", "--all", "--quiet", "--no-pager",
		"--output-fields=MESSAGE,PRIORITY,_PID,_SYSTEMD_UNIT,UNIT,OBJECT_SYSTEMD_UNIT,COREDUMP_UNIT",
	}
	for _, unit := range systemlog.Units(q.Services) {
		args = append(args, "--unit="+unit)
	}
	if !q.Since.IsZero() {
		args = append(args, "--since=@"+strconv.FormatInt(q.Since.Unix(), 10))
	}
	if !q.Until.IsZero() {
		args = append(args, "--until=@"+strconv.FormatInt(q.Until.Unix(), 10))
	}
	switch {
	case q.After != "":
		args = append(args, "--after-cursor="+q.After)
	case q.Before != "":
		// In reverse, the entry "after" a cursor is the next older one.
		args = append(args, "--reverse", "--after-cursor="+q.Before)
	default:
		args = append(args, "--reverse")
	}
	return args
}

// ReadLogs reads one page. Without After it walks back from the newest line
// (or from Before); with After it walks forward from that cursor.
func (r *Reader) ReadLogs(ctx context.Context, q systemlog.Query) (systemlog.Page, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	stream, err := r.open(ctx, Arguments(q))
	if err != nil {
		return systemlog.Page{}, fmt.Errorf("start journalctl: %w", err)
	}
	lines := bufio.NewReaderSize(stream.Output, 64<<10)
	page := systemlog.Page{Items: []systemlog.Entry{}}
	var last systemlog.Entry
	scanned, exhausted, full := 0, false, false
	for scanned < systemlog.ScanLimit {
		line, err := readLine(lines)
		if errors.Is(err, io.EOF) {
			exhausted = true
			break
		}
		if err != nil {
			// A timeout ends the read with what it found so far.
			if ctx.Err() == nil {
				_, _ = stream.Finish(true)
				return systemlog.Page{}, fmt.Errorf("read journalctl: %w", err)
			}
			break
		}
		scanned++
		entry, ok := parseEntry(line)
		if !ok {
			continue
		}
		last = entry
		if !q.Matches(entry) {
			continue
		}
		if q.After != "" {
			page.Items = append([]systemlog.Entry{entry}, page.Items...)
		} else {
			page.Items = append(page.Items, entry)
		}
		if len(page.Items) == q.Limit {
			full = true
			break
		}
	}
	if ctx.Err() != nil {
		// journalctl was killed at the deadline; its end of output is not the
		// start of the journal.
		exhausted = false
		if scanned == 0 {
			_, _ = stream.Finish(true)
			return systemlog.Page{}, fmt.Errorf("read journalctl: %w", ctx.Err())
		}
	}
	stderr, finishErr := stream.Finish(!exhausted)
	if exhausted && finishErr != nil && scanned == 0 && !noMatchingUnits(stderr) {
		return systemlog.Page{}, fmt.Errorf("journalctl: %v: %s", finishErr, strings.TrimSpace(stderr))
	}
	if q.After != "" {
		page.Overflow = full
		return page, nil
	}
	if !exhausted && last.Cursor != "" {
		page.Next = last.Cursor
		if !full {
			scannedThrough := last.Time
			page.ScannedThrough = &scannedThrough
		}
	}
	return page, nil
}

// noMatchingUnits recognises journalctl refusing unit globs that match no
// unit in the journal yet, such as a local console that never ran.
func noMatchingUnits(stderr string) bool {
	return strings.Contains(stderr, "No data available")
}

// readLine returns the next line, skipping the rest of one longer than
// maxLine rather than failing the read.
func readLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) <= maxLine {
			line = append(line, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && (len(line) == 0 || !errors.Is(err, io.EOF)) {
			return nil, err
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
}

// parseEntry reads one journalctl JSON line. Values are strings, byte arrays
// for text that is not UTF-8, or null.
func parseEntry(line []byte) (systemlog.Entry, bool) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(line, &document); err != nil {
		return systemlog.Entry{}, false
	}
	field := func(name string) string {
		raw, ok := document[name]
		if !ok {
			return ""
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return text
		}
		var data []byte
		var numbers []int
		if err := json.Unmarshal(raw, &numbers); err == nil {
			for _, number := range numbers {
				data = append(data, byte(number))
			}
			return strings.ToValidUTF8(string(data), string(utf8.RuneError))
		}
		return ""
	}
	cursor := field("__CURSOR")
	microseconds, err := strconv.ParseInt(field("__REALTIME_TIMESTAMP"), 10, 64)
	if cursor == "" || err != nil {
		return systemlog.Entry{}, false
	}
	entry := systemlog.Entry{Cursor: cursor, Time: time.UnixMicro(microseconds).UTC()}
	for _, name := range []string{"_SYSTEMD_UNIT", "UNIT", "OBJECT_SYSTEMD_UNIT", "COREDUMP_UNIT"} {
		if unit := field(name); unit != "" {
			if service, ok := systemlog.ServiceOfUnit(unit); ok {
				entry.Service, entry.Unit = service, unit
				break
			}
		}
	}
	if entry.Service == "" {
		return systemlog.Entry{}, false
	}
	priority, err := strconv.Atoi(field("PRIORITY"))
	if err != nil {
		priority = 6
	}
	entry.PID, _ = strconv.Atoi(field("_PID"))
	message, truncated := systemlog.TruncateMessage(field("MESSAGE"))
	entry.Level, entry.Message, entry.Fields = systemlog.ParseMessage(message, priority)
	entry.Truncated = truncated
	return entry, true
}

func runJournalctl(ctx context.Context, args []string) (Stream, error) {
	command := exec.CommandContext(ctx, "journalctl", args...)
	command.Env = []string{"LC_ALL=C.UTF-8", "SYSTEMD_COLORS=0", "SYSTEMD_PAGER=cat"}
	output, err := command.StdoutPipe()
	if err != nil {
		return Stream{}, err
	}
	stderr := &limitedBuffer{limit: 4096}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return Stream{}, err
	}
	var once sync.Once
	var waitErr error
	return Stream{Output: output, Finish: func(stop bool) (string, error) {
		once.Do(func() {
			if stop {
				_ = command.Process.Kill()
			}
			waitErr = command.Wait()
		})
		return stderr.String(), waitErr
	}}, nil
}

type limitedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buffer.Len(); room > 0 {
		if len(data) > room {
			b.buffer.Write(data[:room])
		} else {
			b.buffer.Write(data)
		}
	}
	return len(data), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

var _ systemlog.Reader = (*Reader)(nil)
