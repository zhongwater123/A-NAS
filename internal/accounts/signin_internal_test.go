package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestFailureKeysBoundUsernameMemory(t *testing.T) {
	client := "192.0.2.10"
	username := strings.Repeat("a", 1024*1024-100)
	byName, byClient := failureKeys(client, username)
	if len(byName) > len(client)+1+64 || byClient != client {
		t.Fatalf("failure key retains %d bytes for username", len(byName)-len(client))
	}
	other, _ := failureKeys(client, username+"b")
	if other == byName {
		t.Fatal("long usernames must remain distinct")
	}
	var failures signInFailures
	now := time.Now()
	for range freeNameFailures {
		failures.failed(now, client, username)
	}
	if got := failures.wait(now, client, username); got != firstWait {
		t.Fatalf("name wait=%s", got)
	}
	if got := failures.wait(now, client, username+"b"); got != 0 {
		t.Fatalf("other name wait=%s", got)
	}
	for range freeClientFailures - freeNameFailures {
		failures.failed(now, client, username+"b")
	}
	if got := failures.wait(now, client, "alice"); got != firstWait {
		t.Fatalf("client wait=%s", got)
	}
}

func TestFirstDecoyHashSharesPasswordLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		initialized := false
		work := newPasswordWork(func() (string, error) { initialized = true; return "decoy", nil })
		work.verify = func(_, _ string) bool { <-release; return false }
		for range passwordChecks {
			go func() { _, _ = work.matches(t.Context(), "wrong", "real") }()
		}
		synctest.Wait()
		go func() { _, _ = work.matches(t.Context(), "wrong", "") }()
		synctest.Wait()
		if initialized {
			t.Error("first decoy hash ran while all password slots were occupied")
		}
		close(release)
		synctest.Wait()
		if !initialized {
			t.Error("decoy was not initialized after a slot became available")
		}
	})
}

func TestCancelledDecoyDoesNotInitialize(t *testing.T) {
	work := newPasswordWork(func() (string, error) { t.Error("cancelled attempt initialized decoy"); return "decoy", nil })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := work.matches(ctx, "wrong", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestSignInClientQueueCancellationAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var failures signInFailures
		release, err := failures.acquire(t.Context(), "laptop")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			release, err := failures.acquire(ctx, "laptop")
			if err == nil {
				release()
			}
			done <- err
		}()
		synctest.Wait()
		other, err := failures.acquire(t.Context(), "phone")
		if err != nil {
			t.Fatal(err)
		}
		other()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation: %v", err)
		}
		if len(failures.clients) != 1 || failures.clients["laptop"].refs != 1 {
			t.Fatal("cancelled waiter retained its client reference")
		}
		release()
		if len(failures.clients) != 0 {
			t.Fatal("idle clients retained in admission queue")
		}
	})
}
