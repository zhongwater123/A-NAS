//go:build rootintegration

package linux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// holdControlDatabaseArgument makes the test binary act as the Product
// Service: it creates a session in control.db, prints the token, and keeps
// the database open until stdin closes.
const holdControlDatabaseArgument = "hold-control-db"

func init() {
	if len(os.Args) != 3 || os.Args[1] != holdControlDatabaseArgument {
		return
	}
	if err := holdControlDatabase(os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func holdControlDatabase(path string) error {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(path)
	if err != nil {
		return err
	}
	defer store.Close()
	service := accounts.NewService(store, acceptAll{}, accounts.Options{})
	if _, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple"); err != nil && !errors.Is(err, accounts.ErrSetupComplete) {
		return err
	}
	session, err := service.Authenticate(ctx, "owner", "correct horse battery staple")
	if err != nil {
		return err
	}
	fmt.Println(session.Token)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
}

type acceptAll struct{}

func (acceptAll) SetCredential(context.Context, accounts.CredentialRequest) error { return nil }
func (acceptAll) DisableCredential(context.Context, string) error                 { return nil }

// TestRootSessionDirectoryLeavesProductStateUntouched checks that the root
// File Broker reads the Product Service's live SQLite database without
// writing it or leaving root-owned side files the service could not open.
func TestRootSessionDirectoryLeavesProductStateUntouched(t *testing.T) {
	if _, err := exec.Command("id", "a-nas").CombinedOutput(); err != nil {
		run(t, "useradd", "--system", "--no-create-home", "--home-dir", "/var/lib/a-nas", "--shell", "/usr/sbin/nologin", "a-nas")
	}
	stateDirectory, err := os.MkdirTemp("", "a-nas-state-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDirectory) })
	run(t, "chown", "a-nas:a-nas", stateDirectory)
	run(t, "chmod", "0700", stateDirectory)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := "/usr/local/bin/anas-matrix-control-db"
	run(t, "install", "-m", "0755", self, helper)
	database := filepath.Join(stateDirectory, "control.db")
	product := exec.Command("setpriv", "--reuid=a-nas", "--regid=a-nas", "--init-groups", "--", helper, holdControlDatabaseArgument, database)
	stdin, err := product.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := product.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	product.Stderr = os.Stderr
	if err := product.Start(); err != nil {
		t.Fatal(err)
	}
	token, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("Product Service stand-in did not create a session: %v", err)
	}
	token = strings.TrimSpace(token)

	directory := accounts.NewSessionDirectory(database)
	identity, err := directory.ResolveSessionIdentity(context.Background(), token)
	if err != nil {
		t.Fatalf("ResolveSessionIdentity() while the service holds the database error = %v", err)
	}
	if identity.Username != "owner" || identity.UID != accounts.FirstUserUID {
		t.Fatalf("identity = %#v", identity)
	}
	_ = stdin.Close()
	if err := product.Wait(); err != nil {
		t.Fatalf("Product Service stand-in failed: %v", err)
	}
	// The service has closed the database; the broker still holds its
	// read-only connection and is asked again.
	_, _ = directory.ResolveSessionIdentity(context.Background(), token)
	_ = directory.Close()

	entries, err := os.ReadDir(stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if stat := info.Sys().(*syscall.Stat_t); stat.Uid == 0 {
			t.Errorf("the root broker left %s owned by root in the Product Service state", entry.Name())
		}
	}
	// The service must still be able to reopen its database.
	reopen := exec.Command("setpriv", "--reuid=a-nas", "--regid=a-nas", "--init-groups", "--", helper, holdControlDatabaseArgument, database)
	reopen.Stdin = strings.NewReader("\n")
	if output, err := reopen.CombinedOutput(); err != nil {
		t.Fatalf("Product Service cannot reopen its database after the broker: %v\n%s", err, output)
	}
}
