package linux_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
)

// racingGroups is a host without the fixed groups yet. Looking up a missing
// group takes a while, so that two callers not ordered with each other both
// find it missing; groupadd then fails for a group that exists, with
// groupadd's exit status 9.
type racingGroups struct {
	mu     sync.Mutex
	groups map[string]int
}

func (h *racingGroups) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	return h.Output(ctx, name, args, stdin)
}

func (h *racingGroups) Output(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	switch {
	case name == "getent" && args[0] == "group":
		h.mu.Lock()
		gid, found := h.groups[args[1]]
		h.mu.Unlock()
		if found {
			return fmt.Appendf(nil, "%s:x:%d:\n", args[1], gid), nil
		}
		time.Sleep(100 * time.Millisecond)
		return nil, exitStatus(2)
	case name == "getent":
		return nil, exitStatus(2)
	case name == "groupadd":
		gid, _ := strconv.Atoi(args[1])
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, found := h.groups[args[2]]; found {
			return fmt.Appendf(nil, "groupadd: group '%s' already exists\n", args[2]), exitStatus(9)
		}
		h.groups[args[2]] = gid
	}
	return nil, nil
}

// The Host Agent's data-volume reconciliation at startup and the Product
// Service's identity synchronization create the fixed groups at the same
// time; the one that came second failed, and with it the reconciliation that
// writes the Samba configuration.
func TestConcurrentCallersCreateTheFixedGroupsOnce(t *testing.T) {
	host := &racingGroups{groups: map[string]int{}}
	executor := linux.NewExecutor(nil, host, linux.Options{
		SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "offline"),
	})
	var wait sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wait.Go(func() { errs[i] = executor.SyncIdentities(context.Background(), nil) })
	}
	wait.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("SyncIdentities() #%d error = %v", i+1, err)
		}
	}
	if len(host.groups) != 2 {
		t.Fatalf("groups = %v, want both fixed groups", host.groups)
	}
}
