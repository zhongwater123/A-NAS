package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountremoval"
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
		api, remove, closeMontage, err := configuredMontage(t.Context(), Config{Mode: ModeAgent, MontageEnabled: enabled, MontageSocket: socket}, nil)
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
		api, remove, closeMontage, err := configuredMontage(t.Context(), Config{MontageEnabled: enabled, DataMount: dir}, nil)
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
