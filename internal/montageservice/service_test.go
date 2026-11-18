//go:build linux

package montageservice

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

func get(t *testing.T, socket, token string) (int, string) {
	t.Helper()
	client := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 5 * time.Second}
	request, _ := http.NewRequest(http.MethodGet, "http://montage/api/v1/montage/projects", nil)
	if token != "" {
		request.Header.Set(serviceproxy.SessionHeader, token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, strings.TrimSpace(string(body))
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
		done <- Run(ctx, Config{Root: root, SocketPath: socket, Sessions: sessions{}, RetryInterval: 20 * time.Millisecond,
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
			if _, err := os.Stat(socket); err == nil {
				if status, body := get(t, socket, "alice-session"); status == want {
					return body
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("the service did not answer %d", want)
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
