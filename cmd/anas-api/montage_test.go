package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// A deleted account leaves the montage store while the switch is off too:
// rows from a time it was on must not stay (ADR 0038).
func TestDeletedAccountsLeaveTheMontageStoreWhateverTheSwitch(t *testing.T) {
	// Unix socket paths are short; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "montage")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "montage.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte("{}"))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	for _, enabled := range []bool{false, true} {
		api, remove, closeMontage, err := configuredMontage(t.Context(), Config{Mode: ModeAgent, MontageEnabled: enabled, MontageSocket: socket}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if (api != nil) != enabled {
			t.Errorf("switch %t: API mounted = %t", enabled, api != nil)
		}
		if err := remove(t.Context(), "user:bob"); err != nil {
			t.Fatalf("switch %t: %v", enabled, err)
		}
		closeMontage()
	}
	if want := "POST " + accountremoval.Path + "user:bob"; len(asked) != 2 || asked[0] != want || asked[1] != want {
		t.Fatalf("the montage service was asked %q", asked)
	}

	// Development keeps the store in this process, switched off or on.
	for _, enabled := range []bool{false, true} {
		api, remove, closeMontage, err := configuredMontage(t.Context(), Config{MontageEnabled: enabled, DataMount: dir}, idleGrants{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if (api != nil) != enabled {
			t.Errorf("development, switch %t: API mounted = %t", enabled, api != nil)
		}
		if err := remove(t.Context(), "user:bob"); err != nil {
			t.Fatalf("development, switch %t: %v", enabled, err)
		}
		closeMontage()
	}
}

// idleGrants is a photo library that holds no grants.
type idleGrants struct{}

func (idleGrants) Register(context.Context, string, string, []string) (photos.Grant, error) {
	return photos.Grant{}, errors.New("no photo library here")
}
func (idleGrants) Lookup(context.Context, string, []string) ([]photos.GrantedAsset, error) {
	return nil, errors.New("no photo library here")
}
func (idleGrants) Remove(context.Context, string, []string) error { return nil }
func (idleGrants) List(context.Context) ([]photos.Grant, error)   { return nil, nil }
func (idleGrants) Revoke(context.Context, string) error           { return nil }
