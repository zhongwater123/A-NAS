//go:build linux

package sharefiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	"github.com/zhongwater123/A-NAS/internal/filebroker/rootbroker"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/shares"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == rootbroker.WorkerArgument {
		if e := rootbroker.RunWorker(os.Args[2]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "share-acl-helper" {
		if e := aclHelper(os.Args[2:]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func aclHelper(args []string) error {
	switch args[0] {
	case "readonly":
		f, e := os.Open(args[1])
		if e != nil {
			return e
		}
		_ = f.Close()
		f, e = os.OpenFile(args[1], os.O_WRONLY, 0)
		if f != nil {
			_ = f.Close()
			return errors.New("sender writes read-only source")
		}
		if !errors.Is(e, os.ErrPermission) {
			return e
		}
		return nil
	case "capture":
		for _, p := range args[1:3] {
			f, e := os.Open(p)
			if f != nil {
				_ = f.Close()
				return fmt.Errorf("shares identity opened source %s", p)
			}
			if !errors.Is(e, os.ErrPermission) {
				return fmt.Errorf("expected permission refusal for %s: %v", p, e)
			}
		}
		src := os.NewFile(3, "broker-source")
		defer func() { _ = src.Close() }()
		dst, e := os.OpenFile(args[3], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer func() { _ = dst.Close() }()
		if e = unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())); e != nil {
			return e
		}
		if e = dst.Sync(); e != nil {
			return e
		}
		return dst.Chmod(0400)
	case "deny":
		for _, flag := range []int{os.O_RDONLY, os.O_WRONLY} {
			f, e := os.OpenFile(args[1], flag, 0)
			if f != nil {
				_ = f.Close()
				return errors.New("recipient accessed managed share content")
			}
			if !errors.Is(e, os.ErrPermission) {
				return e
			}
		}
		return nil
	case "stream":
		f, e := os.Open(args[1])
		if e != nil {
			return e
		}
		defer func() { _ = f.Close() }()
		_, e = io.Copy(os.Stdout, f)
		return e
	}
	return errors.New("unknown helper mode")
}
func asIdentity(uid uint32, args ...string) *exec.Cmd {
	c := exec.Command(os.Args[0], append([]string{"share-acl-helper"}, args...)...)
	c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: []uint32{}}}
	return c
}
func TestShareACLMatrix(t *testing.T) {
	root := os.Getenv("ANAS_SHARE_BTRFS_ROOT")
	if os.Getenv("ANAS_SHARE_ACL") != "1" || root == "" || os.Geteuid() != 0 {
		t.Skip("requires isolated root Btrfs container")
	}
	base, e := os.MkdirTemp(root, "acl-")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = os.RemoveAll(base) }()
	if e = os.Chmod(base, 0755); e != nil {
		t.Fatal(e)
	}
	state := t.TempDir()
	dbPath := filepath.Join(state, "accounts.db")
	db, e := accounts.OpenSQLite(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	users := accounts.NewService(db, credentials{}, accounts.Options{})
	alice, e := users.SetupAdministrator(context.Background(), "alice", "correct horse battery staple")
	if e != nil {
		t.Fatal(e)
	}
	bob, e := users.CreateMember(context.Background(), alice, "bob", "initial secure password")
	if e != nil {
		t.Fatal(e)
	}
	if e = users.ChangePassword(context.Background(), bob, "initial secure password", "changed secure password"); e != nil {
		t.Fatal(e)
	}
	a, e := users.Authenticate(context.Background(), "alice", "correct horse battery staple")
	if e != nil {
		t.Fatal(e)
	}
	b, e := users.Authenticate(context.Background(), "bob", "changed secure password")
	if e != nil {
		t.Fatal(e)
	}
	ac := accounts.WithSessionToken(context.Background(), a.Token)
	bc := accounts.WithSessionToken(context.Background(), b.Token)
	volume := filepath.Join(base, "volume")
	private := filepath.Join(volume, "spaces", "private")
	shared := filepath.Join(volume, "spaces", "shared")
	shareRoot := filepath.Join(volume, "shares")
	for _, p := range []string{private, shared, filepath.Join(private, "alice"), filepath.Join(private, "bob"), shareRoot} {
		if e = os.MkdirAll(p, 0755); e != nil {
			t.Fatal(e)
		}
	}
	for uid, name := range map[int]string{20100: "alice", 20101: "bob"} {
		p := filepath.Join(private, name)
		if e = os.Chown(p, uid, uid); e != nil {
			t.Fatal(e)
		}
		if e = os.Chmod(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.Chown(shared, 0, 20000); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(shared, 0750); e != nil {
		t.Fatal(e)
	}
	if e = os.Chown(shareRoot, 31001, 31001); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(shareRoot, 0700); e != nil {
		t.Fatal(e)
	}
	privateSource := filepath.Join(private, "alice", "private.txt")
	sharedSource := filepath.Join(shared, "readonly.txt")
	for _, p := range []string{privateSource, sharedSource} {
		if e = os.WriteFile(p, []byte("fixed read-only content"), 0440); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.Chown(privateSource, 20100, 20100); e != nil {
		t.Fatal(e)
	}
	if e = os.Chown(sharedSource, 0, 20000); e != nil {
		t.Fatal(e)
	}
	bobRoot := filepath.Join(private, "bob")
	acl := exec.Command("setfacl", "-m", "d:u::rwx,d:g::---,d:o::---,d:u:23000:r--,d:m::rwx", bobRoot)
	if out, e := acl.CombinedOutput(); e != nil {
		t.Fatalf("set default ACL: %s %v", out, e)
	}
	sessions := accounts.NewSessionDirectory(dbPath)
	defer func() { _ = sessions.Close() }()
	broker, e := rootbroker.NewServer(rootbroker.Config{Sessions: sessions, VolumeRoot: volume, VolumeReady: func() bool { return true }, Command: func(v string) *exec.Cmd { return exec.Command(os.Args[0], rootbroker.WorkerArgument, v) }})
	if e != nil {
		t.Fatal(e)
	}
	defer broker.Close()
	socket := filepath.Join(state, "broker.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = listener.Close() }()
	go func() { _ = broker.Serve(listener) }()
	client := filebroker.NewClient(socket)
	catalog, e := files.OpenSQLite(filepath.Join(state, "files.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = catalog.Close() }()
	fs := files.NewService(catalog, volume, users, client, files.Options{AllowUnverifiedVolume: true, DisableCapacityReserve: true})
	local := NewLocal(fs, users)
	local.SetBroker(client)
	bridgePath := filepath.Join(state, "bridge.sock")
	stop, e := Serve(context.Background(), bridgePath, local)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	remote, e := NewRemote(context.Background(), bridgePath)
	if e != nil {
		t.Fatal(e)
	}
	entries, e := fs.List(ac, alice, "space:shared", "")
	if e != nil || len(entries) != 1 {
		t.Fatalf("readonly source listing: %+v %v", entries, e)
	}
	source, e := remote.Open(ac, user(alice), entries[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = source.Close() }()
	// Prove sender cannot write the shared source through its actual worker.
	if _, _, e = client.CreateTemp(ac, shared, "denied-"); !errors.Is(e, os.ErrPermission) {
		t.Fatalf("sender unexpectedly writes shared directory: %v", e)
	}
	readonly := asIdentity(20100, "readonly", sharedSource)
	readonly.SysProcAttr.Credential.Groups = []uint32{20000, 20001}
	if out, e := readonly.CombinedOutput(); e != nil {
		t.Fatalf("sender source read-only ACL: %s %v", out, e)
	}
	captured := filepath.Join(shareRoot, "captured")
	capture := asIdentity(31001, "capture", privateSource, sharedSource, captured)
	capture.ExtraFiles = []*os.File{source}
	if out, e := capture.CombinedOutput(); e != nil {
		t.Fatalf("read-only import as shares UID: %s %v", out, e)
	}
	deny := asIdentity(20101, "deny", captured)
	if out, e := deny.CombinedOutput(); e != nil {
		t.Fatalf("recipient managed-store isolation: %s %v", out, e)
	}
	spaces, e := users.ListSpaces(bc, bob)
	if e != nil {
		t.Fatal(e)
	}
	var spaceID string
	for _, s := range spaces {
		if s.Kind == accounts.SpaceKindPrivate {
			spaceID = s.ID
		}
	}
	stream := asIdentity(31001, "stream", captured)
	reader, e := stream.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = stream.Start(); e != nil {
		t.Fatal(e)
	}
	pending, e := remote.Stage(bc, user(bob), shares.Target{RootID: spaceID, ShareID: "acl-share"}, shares.Entry{Path: "saved.txt", Size: int64(len("fixed read-only content"))}, reader)
	reader.Close()
	waitErr := stream.Wait()
	if e != nil || waitErr != nil {
		t.Fatalf("stage %v, stream %v", e, waitErr)
	}
	if e = remote.Commit(bc, user(bob), pending); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(bobRoot, "saved.txt")
	info, e := os.Stat(target)
	if e != nil {
		t.Fatal(e)
	}
	if info.Sys().(*syscall.Stat_t).Uid != 20101 {
		t.Fatalf("target owner: %+v", info.Sys())
	}
	out, e := exec.Command("getfacl", "-cpn", target).CombinedOutput()
	if e != nil || !strings.Contains(string(out), "user:23000:r--") {
		t.Fatalf("target default ACL not inherited: %s %v", out, e)
	}
	received := asIdentity(20101, "stream", target)
	out, e = received.CombinedOutput()
	if e != nil || string(out) != "fixed read-only content" {
		t.Fatalf("recipient file: %q %v", out, e)
	}
	t.Log("PASS: UID 20100 reads but cannot create in shared source; UID 31001 cannot open private/shared source, clones only delegated fd; UID 20101 cannot read/write managed content; saved file UID 20101 inherits default user:23000:r-- ACL")
}
