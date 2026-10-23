package linux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

var errTaskObservationLimit = errors.New("observation exceeds read limit")

const taskReadLimit = 1 << 20
const taskTimeout = 4 * time.Second
const taskProcessLimit = 4096
const taskServiceLimit = 512

// A context-aware gate also bounds time spent waiting behind another browser.
type taskGate struct {
	once  sync.Once
	token chan struct{}
}

func (g *taskGate) lock(ctx context.Context) error {
	g.once.Do(func() { g.token = make(chan struct{}, 1); g.token <- struct{}{} })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		return nil
	}
}
func (g *taskGate) unlock() { g.token <- struct{}{} }

type boundedTaskFS struct{ fs.FS }

func (f boundedTaskFS) ReadFile(name string) ([]byte, error) { return readTaskFile(f.FS, name) }
func readTaskFile(root fs.FS, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, taskReadLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > taskReadLimit {
		return nil, errTaskObservationLimit
	}
	return data, nil
}
func taskDirectories(root fs.FS, name string, limit int) ([]fs.DirEntry, bool, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	dir, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, false, errors.New("directory cannot be enumerated")
	}
	entries, err := dir.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if len(entries) > limit {
		return entries[:limit], true, nil
	}
	return entries, false, nil
}

type taskOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (w *taskOutput) Len() int      { return w.buffer.Len() }
func (w *taskOutput) Bytes() []byte { return w.buffer.Bytes() }

func (w *taskOutput) Write(p []byte) (int, error) {
	if len(p) > taskReadLimit-w.Len() {
		w.truncated = true
		return 0, errTaskObservationLimit
	}
	return w.buffer.Write(p)
}
func runTaskCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 100 * time.Millisecond
	out := &taskOutput{}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if out.truncated {
		return nil, errTaskObservationLimit
	}
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func readRunningServiceProperties(ctx context.Context) ([]byte, error) {
	return runTaskCommand(ctx, "systemctl", "show", "--all", "--no-pager", "--property=Id,ActiveState,SubState,ControlGroup,InvocationID,MainPID", "*.service")
}
func readTaskBlockDevices(ctx context.Context) ([]byte, error) {
	return runTaskCommand(ctx, "lsblk", "--json", "--bytes", "--tree", "--exclude", "7", "--output", "NAME,TYPE,SIZE,MODEL,WWN,SERIAL")
}
func taskUint(root fs.FS, path string) (*uint64, error) {
	b, err := readTaskFile(root, path)
	if err != nil {
		return nil, err
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func taskRate(before, after *uint64, seconds float64) *float64 {
	if before == nil || after == nil || *after < *before || seconds <= 0 {
		return nil
	}
	return new(float64(*after-*before) / seconds)
}

func taskNumber(v string) (uint64, error) { return strconv.ParseUint(v, 10, 64) }
func taskCPUPercent(before, after *counterSample) *float64 {
	if after.cpuTotal <= before.cpuTotal || after.cpuBusy < before.cpuBusy || after.logicalCores != before.logicalCores {
		return nil
	}
	return new(cpuUsagePercent(before, after))
}
func taskCPU(root fs.FS, at time.Time) (*counterSample, error) {
	busy, total, cores, err := readCPU(boundedTaskFS{root})
	if err != nil {
		return nil, fmt.Errorf("read task CPU: %w", err)
	}
	return &counterSample{at: at, cpuBusy: busy, cpuTotal: total, logicalCores: cores}, nil
}

var _ taskmanager.Reader = (*Reader)(nil)
