//go:build linux

package storeservice_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/storeservice"
)

type sessions struct{}

func (sessions) ResolveSessionUser(_ context.Context, token string) (accounts.SessionUser, error) {
	if token != "alice-session" {
		return accounts.SessionUser{}, accounts.ErrSessionNotFound
	}
	return accounts.SessionUser{UserID: "user:alice", Username: "alice", Role: accounts.RoleMember}, nil
}
func (sessions) ActiveProfiles(context.Context) ([]accounts.Profile, error) { return nil, nil }
func (sessions) AccountDeletion(context.Context, string) (accountid.AccountDeletion, error) {
	return accountid.AccountDeletion{}, errors.New("no deletions")
}

type store struct{ closed atomic.Bool }

func (s *store) Close() error { s.closed.Store(true); return nil }

type service struct {
	t       *testing.T
	socket  string
	usable  atomic.Bool
	opened  chan *store
	entered chan struct{}
	release chan struct{}
	upkept  atomic.Int32
	// stuck makes upkeep wait until it is stopped, as upkeep waiting on
	// another service does; closedUnder is set if its store closed first.
	stuck       bool
	closedUnder atomic.Bool
}

// start runs a service whose API answers /quick at once and /slow once
// release closes, each saying whether its store was still open.
func start(t *testing.T, stuck bool) *service {
	t.Helper()
	// Unix socket paths are short; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "store")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	root := filepath.Join(dir, "store")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &service{t: t, socket: filepath.Join(dir, "api.sock"), opened: make(chan *store, 4),
		entered: make(chan struct{}, 1), release: make(chan struct{}), stuck: stuck}
	s.usable.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- storeservice.Run(ctx, storeservice.Config[*store]{
			Name: "test", Root: root, SocketPath: s.socket, Sessions: sessions{}, Unavailable: "test_unavailable",
			Open: func(context.Context, string) (*store, error) {
				opened := &store{}
				s.opened <- opened
				return opened, nil
			},
			API: func(opened *store) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/slow" {
						s.entered <- struct{}{}
						<-s.release
					}
					if opened.closed.Load() {
						_, _ = io.WriteString(w, "closed")
						return
					}
					_, _ = io.WriteString(w, "open")
				})
			},
			Remove: func(*store) accountremoval.Remove {
				return func(context.Context, accountid.AccountDeletion) (accountremoval.Result, error) {
					return accountremoval.Result{}, nil
				}
			},
			Upkeep: func(ctx context.Context, opened *store) {
				s.upkept.Add(1)
				if s.stuck {
					<-ctx.Done()
					s.closedUnder.Store(opened.closed.Load())
				}
			},
			UpkeepInterval: time.Millisecond,
			CheckStore: func(string) error {
				if s.usable.Load() {
					return nil
				}
				return errors.New("the data volume is away")
			},
			RetryInterval: 10 * time.Millisecond,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return s
}

func (s *service) get(path, token string) (int, string) {
	s.t.Helper()
	client := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.socket)
	}}, Timeout: 5 * time.Second}
	request, _ := http.NewRequest(http.MethodGet, "http://service"+path, nil)
	if token != "" {
		request.Header.Set(serviceproxy.SessionHeader, token)
	}
	response, err := client.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, strings.TrimSpace(string(body))
}

func (s *service) waitFor(want int) string {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(s.socket); err == nil {
			if status, body := s.get("/quick", "alice-session"); status == want {
				return body
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("the service did not answer %d", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAStoreServiceConfirmsSessionsAndFollowsItsStore(t *testing.T) {
	s := start(t, false)
	if body := s.waitFor(http.StatusOK); body != "open" {
		t.Fatalf("served %q", body)
	}
	if info, err := os.Stat(s.socket); err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode = %v, %v", info.Mode(), err)
	}
	for _, token := range []string{"", "forged"} {
		if status, _ := s.get("/quick", token); status != http.StatusUnauthorized {
			t.Fatalf("session %q = %d", token, status)
		}
	}
	first := <-s.opened
	eventually(t, "the open store gets its upkeep", func() bool { return s.upkept.Load() > 0 })
	s.usable.Store(false)
	if body := s.waitFor(http.StatusServiceUnavailable); !strings.Contains(body, "test_unavailable") {
		t.Fatalf("unavailable store = %s", body)
	}
	eventually(t, "the store closes once out of service", first.closed.Load)
	s.usable.Store(true)
	s.waitFor(http.StatusOK)
	if second := <-s.opened; second == first || second.closed.Load() {
		t.Fatal("the store did not open again")
	}
}

// A request in progress as the data volume goes away finishes on the open
// store; the store closes after it, and new requests answer unavailable
// meanwhile.
func TestTakingAStoreOutOfServiceWaitsForItsRequests(t *testing.T) {
	s := start(t, false)
	s.waitFor(http.StatusOK)
	first := <-s.opened
	slow := make(chan string, 1)
	go func() {
		_, body := s.get("/slow", "alice-session")
		slow <- body
	}()
	<-s.entered
	s.usable.Store(false)
	s.waitFor(http.StatusServiceUnavailable)
	if first.closed.Load() {
		t.Fatal("the store closed under a request in progress")
	}
	close(s.release)
	if body := <-slow; body != "open" {
		t.Fatalf("the request in progress found its store %s", body)
	}
	eventually(t, "the store closes after the request", first.closed.Load)
}

func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Upkeep that waits on another service does not keep a store in service
// whose data volume went away, and the store closes only once it stopped.
func TestUpkeepDoesNotHoldAStoreThatWentAway(t *testing.T) {
	s := start(t, true)
	s.waitFor(http.StatusOK)
	first := <-s.opened
	eventually(t, "the open store gets its upkeep", func() bool { return s.upkept.Load() > 0 })
	s.usable.Store(false)
	s.waitFor(http.StatusServiceUnavailable)
	eventually(t, "the store closes once out of service", first.closed.Load)
	if s.closedUnder.Load() {
		t.Fatal("the store closed while its upkeep was running")
	}
}
