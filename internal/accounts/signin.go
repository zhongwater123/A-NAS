package accounts

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
)

// Signing in is bounded in three ways (#175). A client that keeps failing
// waits before each further attempt, so it can neither guess passwords
// without limit nor fill the audit log with failures. At most
// passwordChecks argon2id computations run at once, each holding its
// PasswordCost in memory. And an attempt for an account that cannot sign in
// spends one password check all the same, so its timing does not tell
// whether the account exists.

// ErrSignInThrottled reports an attempt refused because its client failed
// too often; errors.AsType finds the wait in a *ThrottledError.
var ErrSignInThrottled = errors.New("too many failed sign-ins from this client")

// ThrottledError is ErrSignInThrottled with how long the client must wait.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return ErrSignInThrottled.Error() + ": retry after " + e.RetryAfter.String()
}

func (e *ThrottledError) Unwrap() error { return ErrSignInThrottled }

// RetryAfterSeconds is the wait in whole seconds, rounded up, for a
// Retry-After header.
func (e *ThrottledError) RetryAfterSeconds() string {
	return strconv.FormatInt(int64((e.RetryAfter+time.Second-1)/time.Second), 10)
}

const (
	// passwordChecks is how many argon2id computations may run at once:
	// 128 MiB with DefaultPasswordCost. The others queue.
	passwordChecks = 2
	// A client may fail freeNameFailures times in a row for one account name,
	// and freeClientFailures times for all names together, which stops it
	// cycling through names. Each further failure waits, from firstWait
	// doubling up to maxWait.
	freeNameFailures   = 5
	freeClientFailures = 20
	firstWait          = 30 * time.Second
	maxWait            = 15 * time.Minute
	// A client's failures are forgotten forgetAfter after its last one, and
	// at most maxTrackedFailures are kept.
	forgetAfter        = 24 * time.Hour
	maxTrackedFailures = 10_000
)

// signInFailures counts consecutive failed sign-ins per client and per
// client and account name. Clients are what sessions are bound to: the
// direct loopback connection, or the LAN address (an IPv6 client's /64).
type signInFailures struct {
	mu      sync.Mutex
	records map[string]*failureRecord
}

type failureRecord struct {
	count int
	last  time.Time
	until time.Time
}

func failureKeys(client, username string) (byName, byClient string) {
	return client + "\x00" + username, client
}

// wait returns how long the client must still wait before it may try the
// name, or zero.
func (f *signInFailures) wait(now time.Time, client, username string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var wait time.Duration
	byName, byClient := failureKeys(client, username)
	for _, key := range []string{byName, byClient} {
		if record := f.records[key]; record != nil {
			wait = max(wait, record.until.Sub(now))
		}
	}
	return wait
}

// failed counts a failed attempt and sets the wait before the next one.
func (f *signInFailures) failed(now time.Time, client, username string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.records == nil {
		f.records = map[string]*failureRecord{}
	}
	byName, byClient := failureKeys(client, username)
	for key, free := range map[string]int{byName: freeNameFailures, byClient: freeClientFailures} {
		record := f.records[key]
		if record == nil {
			f.makeRoom(now)
			record = &failureRecord{}
			f.records[key] = record
		}
		record.count++
		record.last = now
		if excess := record.count - free; excess >= 0 {
			wait := maxWait
			if excess < 16 {
				wait = min(firstWait<<excess, maxWait)
			}
			record.until = now.Add(wait)
		}
	}
}

// succeeded forgets the failures that a successful sign-in ends.
func (f *signInFailures) succeeded(client, username string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	byName, byClient := failureKeys(client, username)
	delete(f.records, byName)
	delete(f.records, byClient)
}

// makeRoom drops records idle for forgetAfter and, when that is not enough,
// those that are not waiting, before one more is added. Called under mu.
func (f *signInFailures) makeRoom(now time.Time) {
	if len(f.records) < maxTrackedFailures {
		return
	}
	for key, record := range f.records {
		if now.Sub(record.last) > forgetAfter {
			delete(f.records, key)
		}
	}
	for key, record := range f.records {
		if len(f.records) < maxTrackedFailures {
			return
		}
		if !record.until.After(now) {
			delete(f.records, key)
		}
	}
}

// passwordWork runs argon2id computations, passwordChecks at a time.
type passwordWork struct {
	gate *semaphore.Weighted
	// verify is verifyPassword; tests count and pace the calls.
	verify func(password, encoded string) bool
	// decoy is a hash of a random password at the configured cost, checked
	// for attempts that have no hash to check.
	decoy func() (string, error)
}

func newPasswordWork(random func() (string, error)) *passwordWork {
	return &passwordWork{gate: semaphore.NewWeighted(passwordChecks), verify: verifyPassword, decoy: sync.OnceValues(random)}
}

// matches reports whether password is the one encoded hashes. An empty
// encoded checks the decoy instead, which never matches.
func (w *passwordWork) matches(ctx context.Context, password, encoded string) (bool, error) {
	decoy := encoded == ""
	if decoy {
		var err error
		if encoded, err = w.decoy(); err != nil {
			return false, err
		}
	}
	if err := w.gate.Acquire(ctx, 1); err != nil {
		return false, err
	}
	defer w.gate.Release(1)
	return w.verify(password, encoded) && !decoy, nil
}

// hash encodes a new password at the configured cost.
func (w *passwordWork) hash(ctx context.Context, hash func() (string, error)) (string, error) {
	if err := w.gate.Acquire(ctx, 1); err != nil {
		return "", err
	}
	defer w.gate.Release(1)
	return hash()
}
