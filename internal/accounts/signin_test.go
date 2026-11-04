package accounts_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/clocktest"
)

const signInPassword = "alice password for testing"

func TestConcurrentFailuresCannotBypassSignInBackoff(t *testing.T) {
	for _, cycling := range []bool{false, true} {
		t.Run(fmt.Sprintf("cycling=%t", cycling), func(t *testing.T) {
			clock := clocktest.New(time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC))
			service, admin := newSignInService(t, clock.Now)
			client := fromLAN("192.0.2.10")
			limit := 5
			name := func(i int) string {
				if cycling {
					return fmt.Sprintf("nobody%d", i)
				}
				return "alice"
			}
			if cycling {
				limit = 20
			}
			for i := range limit - 1 {
				if _, err := service.Authenticate(client, name(i), "wrong"); !errors.Is(err, accounts.ErrInvalidCredentials) {
					t.Fatal(err)
				}
			}
			var checks atomic.Int32
			for batch := range 2 {
				if batch > 0 {
					clock.Advance(30 * time.Second)
				}
				synctest.Test(t, func(t *testing.T) {
					release := make(chan struct{})
					accounts.SetPasswordVerifier(service, func(_, _ string) bool {
						checks.Add(1)
						<-release
						return false
					})
					results := make(chan error, 10)
					for i := range 10 {
						go func() {
							_, err := service.Authenticate(client, name(limit+i), "wrong")
							results <- err
						}()
					}
					synctest.Wait()
					close(release)
					failures, throttled := 0, 0
					for range 10 {
						switch err := <-results; {
						case errors.Is(err, accounts.ErrInvalidCredentials):
							failures++
						case errors.Is(err, accounts.ErrSignInThrottled):
							throttled++
						default:
							t.Fatalf("unexpected result: %v", err)
						}
					}
					if failures != 1 || throttled != 9 {
						t.Fatalf("failures=%d throttled=%d, want 1 and 9", failures, throttled)
					}
				})
			}
			if got := checks.Load(); got != 2 {
				t.Fatalf("password checks=%d, want 2", got)
			}
			events, err := service.Activity(context.Background(), admin, activity.Query{Limit: activity.MaxLimit})
			if err != nil {
				t.Fatal(err)
			}
			failures := 0
			for _, event := range events {
				if event.Action == "session.sign_in_failed" {
					failures++
				}
			}
			if failures != limit+1 {
				t.Fatalf("audit failures=%d, want %d", failures, limit+1)
			}
		})
	}
}

// newSignInService has an administrator and a member, alice.
func newSignInService(t *testing.T, now func() time.Time) (*accounts.Service, accounts.User) {
	t.Helper()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &viewingRecorder{}, accounts.Options{PasswordCost: cheapPasswords, Now: now})
	ctx := context.Background()
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateMember(ctx, admin, "alice", signInPassword); err != nil {
		t.Fatal(err)
	}
	return service, admin
}

func fromLAN(address string) context.Context {
	return accounts.WithClientOrigin(context.Background(), accounts.ClientOrigin{Via: accounts.ViaLAN, Address: address})
}

func TestAClientThatKeepsFailingWaitsBeforeTryingAgain(t *testing.T) {
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	service, _ := newSignInService(t, func() time.Time { return now })
	laptop, phone := fromLAN("192.0.2.10"), fromLAN("192.0.2.11")
	fail := func() {
		t.Helper()
		if _, err := service.Authenticate(laptop, "alice", "not alice's password"); !errors.Is(err, accounts.ErrInvalidCredentials) {
			t.Fatalf("wrong password: %v", err)
		}
	}
	waits := func(want time.Duration) {
		t.Helper()
		_, err := service.Authenticate(laptop, "alice", signInPassword)
		throttled, ok := errors.AsType[*accounts.ThrottledError](err)
		if !ok || !errors.Is(err, accounts.ErrSignInThrottled) || throttled.RetryAfter != want {
			t.Fatalf("right password while waiting: %v, want a wait of %s", err, want)
		}
	}

	for range 5 {
		fail()
	}
	waits(30 * time.Second)
	if _, err := service.Authenticate(phone, "alice", signInPassword); err != nil {
		t.Fatalf("another client waited too: %v", err)
	}
	now = now.Add(10 * time.Second)
	waits(20 * time.Second)

	now = now.Add(20 * time.Second)
	fail()
	waits(time.Minute)
	now = now.Add(time.Minute)
	if _, err := service.Authenticate(laptop, "alice", signInPassword); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	// Signing in forgets the failures.
	fail()
	if _, err := service.Authenticate(laptop, "alice", signInPassword); err != nil {
		t.Fatalf("after one more failure: %v", err)
	}
}

func TestFailuresAreForgottenADayAfterTheLastOne(t *testing.T) {
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	service, _ := newSignInService(t, func() time.Time { return now })
	laptop := fromLAN("192.0.2.10")
	for range 4 {
		if _, err := service.Authenticate(laptop, "alice", "not alice's password"); !errors.Is(err, accounts.ErrInvalidCredentials) {
			t.Fatalf("wrong password: %v", err)
		}
	}
	now = now.Add(25 * time.Hour)
	if _, err := service.Authenticate(laptop, "alice", "not alice's password"); !errors.Is(err, accounts.ErrInvalidCredentials) {
		t.Fatalf("a day later: %v", err)
	}
	if _, err := service.Authenticate(laptop, "alice", signInPassword); err != nil {
		t.Fatalf("one failure after a day made the client wait: %v", err)
	}
}

func TestAWaitDoublesUpToFifteenMinutes(t *testing.T) {
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	service, _ := newSignInService(t, func() time.Time { return now })
	laptop := fromLAN("192.0.2.10")
	var last time.Duration
	for range 20 {
		_, err := service.Authenticate(laptop, "alice", "not alice's password")
		if throttled, ok := errors.AsType[*accounts.ThrottledError](err); ok {
			last = throttled.RetryAfter
			now = now.Add(last)
		}
	}
	if last != 15*time.Minute {
		t.Fatalf("longest wait = %s, want 15m", last)
	}
}

func TestAClientCyclingThroughNamesWaitsAndStopsFillingTheAuditLog(t *testing.T) {
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	service, admin := newSignInService(t, func() time.Time { return now })
	laptop := fromLAN("192.0.2.10")
	for i := range 20 {
		if _, err := service.Authenticate(laptop, fmt.Sprintf("nobody%d", i), "any password at all"); !errors.Is(err, accounts.ErrInvalidCredentials) {
			t.Fatalf("unknown name %d: %v", i, err)
		}
	}
	for i := range 3 {
		if _, err := service.Authenticate(laptop, fmt.Sprintf("someone%d", i), "any password at all"); !errors.Is(err, accounts.ErrSignInThrottled) {
			t.Fatalf("21st name: %v", err)
		}
	}
	if _, err := service.Authenticate(laptop, "alice", signInPassword); !errors.Is(err, accounts.ErrSignInThrottled) {
		t.Fatalf("a real account from the same client: %v", err)
	}
	if _, err := service.Authenticate(fromLAN("192.0.2.11"), "alice", signInPassword); err != nil {
		t.Fatalf("another client: %v", err)
	}
	events, err := service.Activity(context.Background(), admin, activity.Query{Limit: activity.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, event := range events {
		if event.Action == "session.sign_in_failed" {
			failed++
		}
	}
	if failed != 20 {
		t.Fatalf("%d sign-in failures recorded, want the 20 that ran", failed)
	}
}

func TestEveryAttemptSpendsOnePasswordCheck(t *testing.T) {
	service, admin := newSignInService(t, nil)
	ctx := context.Background()
	var checks atomic.Int32
	accounts.SetPasswordVerifier(service, func(password, encoded string) bool {
		checks.Add(1)
		return accounts.VerifyPassword(password, encoded)
	})
	bob, err := service.CreateMember(ctx, admin, "bob", "bob password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DisableUser(ctx, admin, bob.ID); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct{ name, username, password string }{
		{"unknown account", "nobody", "any password at all"},
		{"disabled account", "bob", "bob password for testing"},
		{"wrong password", "alice", "not alice's password"},
	} {
		checks.Store(0)
		client := fromLAN("192.0.2.10")
		if _, err := service.Authenticate(client, attempt.username, attempt.password); !errors.Is(err, accounts.ErrInvalidCredentials) {
			t.Fatalf("%s: %v", attempt.name, err)
		}
		if got := checks.Load(); got != 1 {
			t.Fatalf("%s: %d password checks, want 1", attempt.name, got)
		}
	}
}

func TestAtMostTwoPasswordChecksRunAtOnce(t *testing.T) {
	service, _ := newSignInService(t, nil)
	release := make(chan struct{})
	entered := make(chan struct{}, 6)
	var running, most atomic.Int32
	accounts.SetPasswordVerifier(service, func(password, encoded string) bool {
		n := running.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		running.Add(-1)
		return accounts.VerifyPassword(password, encoded)
	})
	var attempts sync.WaitGroup
	for i := range 6 {
		attempts.Go(func() {
			_, _ = service.Authenticate(fromLAN(fmt.Sprintf("192.0.2.%d", 20+i)), "alice", signInPassword)
		})
	}
	<-entered
	<-entered
	select {
	case <-entered:
		t.Fatal("a third password check started")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	attempts.Wait()
	if got := most.Load(); got != 2 {
		t.Fatalf("at most %d checks ran at once, want 2", got)
	}
}
