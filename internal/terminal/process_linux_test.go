package terminal

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func startGroup(t *testing.T, script string) *ProcessGroup {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return WatchProcessGroup(cmd)
}

func TestProcessGroupHangUpAfterExitReturnsTheCodeWithoutSignaling(t *testing.T) {
	group := startGroup(t, "exit 3")
	<-group.Done()
	group.mu.Lock()
	reaped := group.reaped
	group.mu.Unlock()
	if !reaped {
		t.Fatal("Done closed before the shell was reaped")
	}
	started := time.Now()
	if code := group.HangUp(time.Minute); code != 3 {
		t.Fatalf("HangUp() = %d, want the exit code 3", code)
	}
	if time.Since(started) > time.Second {
		t.Fatal("HangUp waited for a shell that had already exited")
	}
}

func TestProcessGroupHangUpEscalatesWhenTheShellIgnoresIt(t *testing.T) {
	group := startGroup(t, `trap "" HUP; sleep 60`)
	time.Sleep(100 * time.Millisecond)
	done := make(chan int, 1)
	go func() { done <- group.HangUp(200 * time.Millisecond) }()
	select {
	case code := <-done:
		if code != -1 {
			t.Fatalf("HangUp() = %d, want -1 for a killed shell", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HangUp did not kill a shell that ignores SIGHUP")
	}
}
