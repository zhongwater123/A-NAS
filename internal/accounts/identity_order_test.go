package accounts_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// slowSyncHost is a Host Agent whose identity synchronization takes a while,
// as on a device: it makes the host match the accounts it was given once the
// test lets it finish. smb is what the host ends up with for each account.
type slowSyncHost struct {
	*lifecycleHost
	mu      sync.Mutex
	smb     map[string]bool
	reading chan struct{}
	finish  chan struct{}
}

func (h *slowSyncHost) set(username string, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.smb[username] = on
}

func (h *slowSyncHost) SyncIdentities(_ context.Context, identities []accounts.Identity) error {
	close(h.reading)
	<-h.finish
	for _, identity := range identities {
		h.set(identity.Username, identity.Enabled)
	}
	return nil
}

func (h *slowSyncHost) DisableCredential(_ context.Context, username string) error {
	h.set(username, false)
	return nil
}

func (h *slowSyncHost) EnableCredential(_ context.Context, request accounts.EnableRequest) error {
	h.set(request.Username, request.SMB)
	return nil
}

// A synchronization reads the accounts, then the Host Agent makes the host
// match. An account disabled or enabled after the read and before the host
// was done must keep what the administrator chose: the older picture the
// synchronization read is not the last word. The system test's "the disabled
// member cannot open SMB" failed on this, one run in six, after #506.
func TestAChangeDuringIdentitySynchronizationIsTheLastWordOnTheHost(t *testing.T) {
	for _, change := range []struct {
		name    string
		disable bool
	}{{"disabling", true}, {"enabling", false}} {
		t.Run(change.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			host := &slowSyncHost{lifecycleHost: &lifecycleHost{}, smb: map[string]bool{}, reading: make(chan struct{}), finish: make(chan struct{})}
			service := accounts.NewService(store, host, accounts.Options{PasswordCost: cheapPasswords})
			admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
			if err != nil {
				t.Fatal(err)
			}
			alice, err := service.CreateMember(ctx, admin, "alice", alicePassword)
			if err != nil {
				t.Fatal(err)
			}
			if !change.disable {
				// She starts disabled, and the synchronization reads her so.
				if err := service.DisableUser(ctx, admin, alice.ID); err != nil {
					t.Fatal(err)
				}
			}

			synced := make(chan error, 1)
			go func() { synced <- service.SyncIdentities(ctx) }()
			<-host.reading
			changed := make(chan error, 1)
			go func() {
				if change.disable {
					changed <- service.DisableUser(ctx, admin, alice.ID)
				} else {
					changed <- service.EnableUser(ctx, admin, alice.ID)
				}
			}()
			// Time for the change to reach the host, should it not wait for
			// the synchronization.
			var changeErr error
			early := false
			select {
			case changeErr = <-changed:
				early = true
			case <-time.After(200 * time.Millisecond):
			}
			close(host.finish)
			if err := <-synced; err != nil {
				t.Fatalf("SyncIdentities() error = %v", err)
			}
			if !early {
				changeErr = <-changed
			}
			if changeErr != nil {
				t.Fatalf("%s error = %v", change.name, changeErr)
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if on := host.smb["alice"]; on == change.disable {
				t.Fatalf("after %s during a synchronization, SMB on = %v: the synchronization undid it on the host", change.name, on)
			}
		})
	}
}
