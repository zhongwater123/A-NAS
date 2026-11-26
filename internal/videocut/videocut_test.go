//go:build linux

package videocut

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
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
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

// noGrants is a photo library that holds no grants yet.
type noGrants struct{}

func (noGrants) Register(context.Context, string, string, []string) (photos.Grant, error) {
	return photos.Grant{}, errors.New("not here")
}
func (noGrants) Lookup(context.Context, string, []string) ([]photos.GrantedAsset, error) {
	return nil, errors.New("not here")
}
func (noGrants) Remove(context.Context, string, []string) error { return nil }
func (noGrants) List(context.Context) ([]photos.Grant, error)   { return nil, nil }
func (noGrants) Revoke(context.Context, string) error           { return nil }
func (noGrants) Open(context.Context, string, string) (*os.File, string, error) {
	return nil, "", errors.New("no grant")
}
func (noGrants) Analyze(context.Context, string, []string, bool) error { return nil }
func (noGrants) Analysis(context.Context, string, string) (photos.GrantAnalysis, []byte, error) {
	return photos.GrantAnalysis{}, nil, errors.New("no grant")
}
func (noGrants) SaveFilm(context.Context, string, string, string, *os.File) (string, error) {
	return "", errors.New("no grant")
}

func get(t *testing.T, socket, token string) (int, string) {
	t.Helper()
	status, body, err := request(socket, token)
	if err != nil {
		t.Fatal(err)
	}
	return status, body
}

func request(socket, token string) (int, string, error) {
	client := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 5 * time.Second}
	request, _ := http.NewRequest(http.MethodGet, "http://montage/api/v1/montage/projects", nil)
	if token != "" {
		request.Header.Set(serviceproxy.SessionHeader, token)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, strings.TrimSpace(string(body)), nil
}

func TestTheServiceConfirmsSessionsAndFollowsItsStore(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "montage")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "montage.sock")
	var usable atomic.Bool
	usable.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Root: root, SocketPath: socket, Sessions: sessions{}, Grants: noGrants{}, RetryInterval: 20 * time.Millisecond,
			CheckStore: func(string) error {
				if usable.Load() {
					return nil
				}
				return ErrStoreUnavailable
			}})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitFor := func(want int) string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			// The socket file appears when it is bound, a moment before the
			// service listens on it: until then a connection is refused.
			status, body, err := request(socket, "alice-session")
			if err == nil && status == want {
				return body
			}
			if time.Now().After(deadline) {
				t.Fatalf("the service did not answer %d: %d %s %v", want, status, body, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if body := waitFor(http.StatusOK); body != `{"items":[]}` {
		t.Fatalf("projects = %s", body)
	}
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode = %v, %v", info.Mode(), err)
	}
	if status, _ := get(t, socket, ""); status != http.StatusUnauthorized {
		t.Fatalf("without a session = %d", status)
	}
	if status, _ := get(t, socket, "forged"); status != http.StatusUnauthorized {
		t.Fatalf("with an unknown session = %d", status)
	}
	// The data volume goes read-only: the service answers unavailable, and
	// serves again once it is back.
	usable.Store(false)
	if body := waitFor(http.StatusServiceUnavailable); !strings.Contains(body, "montage_unavailable") {
		t.Fatalf("unavailable store = %s", body)
	}
	usable.Store(true)
	waitFor(http.StatusOK)
}

func TestTheStoreMustBeTheServicesOwnOnTheDataVolume(t *testing.T) {
	// A temporary directory is not on the Btrfs data volume.
	if err := RequireOwnedStore(t.TempDir()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("RequireOwnedStore(tmp) = %v", err)
	}
}
