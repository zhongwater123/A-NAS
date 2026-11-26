//go:build linux

package app_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// The real Worker dies after receiving the catalog request and its memfd,
// before it can reply. Keeping the listening descriptor across processes
// reproduces systemd socket activation on each scheduler retry.
func TestPersistentJobRetriesKilledComputeProcess(t *testing.T) {
	python := os.Getenv("ANAS_MONTAGE_PYTHON")
	if python == "" {
		python = "python3"
	}
	if output, err := exec.Command(python, "-c", "import numpy, PIL").CombinedOutput(); err != nil {
		if os.Getenv("ANAS_MONTAGE_PYTHON") != "" {
			t.Fatalf("configured Worker Python: %v: %s", err, output)
		}
		t.Skip("requires the montage Worker Python with NumPy and Pillow")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	socket := fmt.Sprintf("@a-nas-montage-job-kill-%d", os.Getpid())
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	inherited, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inherited.Close() }()

	env := newJobEnv(t)
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	open := func() {
		t.Helper()
		s, err := app.Open(ctx, env.root, app.Options{Grants: env.lib, JobCompute: videocutcompute.Client{SocketPath: socket},
			Editing: env.editing, Planner: env.planner, Now: env.clock.now})
		if err != nil {
			t.Fatal(err)
		}
		s.UseMusic(music{})
		t.Cleanup(func() { _ = s.Close() })
		env.store = s
	}
	open()
	db := openDB(t, env.root)
	job := env.generate(t, "kill:worker")
	var originalKey string
	for attempt := 1; attempt <= 3; attempt++ {
		worker := exec.CommandContext(ctx, python, "-c", `
import os
import signal
from anas_videocut import worker

def terminate(request, arrays):
    os.kill(os.getpid(), signal.SIGKILL)

os.environ["LISTEN_PID"] = str(os.getpid())
worker.OPS["catalog"] = terminate
worker.main()
`)
		worker.Dir = filepath.Join("..", "..", "..", "ai")
		worker.Env = append(os.Environ(), "LISTEN_FDS=1", "PYTHONDONTWRITEBYTECODE=1")
		worker.ExtraFiles = []*os.File{inherited}
		worker.Stderr = os.Stderr
		if err := worker.Start(); err != nil {
			t.Fatal(err)
		}
		if !env.store.RunOnce(ctx) {
			_ = worker.Process.Kill()
			_ = worker.Wait()
			t.Fatalf("attempt %d was not due", attempt)
		}
		err := worker.Wait()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || ctx.Err() != nil || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("attempt %d: Worker exit %v, context %v", attempt, err, ctx.Err())
		}
		var attempts int
		var key string
		if err := db.QueryRowContext(ctx, `SELECT j.attempts, s.input_key FROM jobs j JOIN steps s ON s.job_id = j.id AND s.name = 'catalog' WHERE j.id = ?`, job.ID).Scan(&attempts, &key); err != nil {
			t.Fatal(err)
		}
		if attempt == 1 {
			originalKey = key
		}
		if attempts != attempt || key == "" || key != originalKey {
			t.Fatalf("attempt %d: persisted attempts=%d key=%q, original=%q", attempt, attempts, key, originalKey)
		}
		got := env.job(t, job.ID)
		want := app.JobQueued
		if attempt == 3 {
			want = app.JobFailed
		}
		if got.Step != "catalog" || got.State != want || got.Version != nil {
			t.Fatalf("attempt %d: job=%+v", attempt, got)
		}
		if attempt == 1 {
			if err := env.store.Close(); err != nil {
				t.Fatal(err)
			}
			open()
		}
		env.clock.advance(time.Duration(attempt)*time.Minute - time.Second)
		if env.store.RunOnce(ctx) {
			t.Fatalf("attempt %d was followed by a premature retry", attempt)
		}
		env.clock.advance(time.Second)
	}
	if env.store.RunOnce(ctx) || env.lib.saves != 0 || env.editing.segments != 0 {
		t.Fatal("failed job continued after three Worker exits")
	}
}
