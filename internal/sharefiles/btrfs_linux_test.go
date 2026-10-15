//go:build linux

package sharefiles

import (
	"bytes"
	"context"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// This test is run only on a disposable, explicitly supplied Btrfs mount.
func TestBtrfsCaptureAtomicAndPublishedCopyIndependent(t *testing.T) {
	root := os.Getenv("ANAS_SHARE_BTRFS_ROOT")
	if root == "" {
		t.Skip("requires disposable Btrfs mount")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil || uint64(stat.Type) != 0x9123683e {
		t.Fatalf("not Btrfs: %v", err)
	}
	t.Setenv("TMPDIR", root)
	f := setup(t)
	a := bytes.Repeat([]byte("A"), 1<<20)
	b := bytes.Repeat([]byte("B"), 1<<20)
	entry, e := f.files.Upload(f.ac, f.alice, f.as, "", "version.bin", bytes.NewReader(a))
	if e != nil {
		t.Fatal(e)
	}
	fd, e := f.remote.Open(f.ac, user(f.alice), entry.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer fd.Close()
	flags, e := unix.FcntlInt(fd.Fd(), unix.F_GETFL, 0)
	if e != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		t.Fatalf("source descriptor not read-only: %d %v", flags, e)
	}
	source := filepath.Join(f.volume, "spaces", "private", "alice", "version.bin")
	writer, e := os.OpenFile(source, os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	writeErr := make(chan error, 1)
	var once sync.Once
	finish := func() { once.Do(func() { close(stop); wg.Wait(); writer.Close() }) }
	t.Cleanup(finish)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, data := range [][]byte{a, b} {
				if _, e := writer.WriteAt(data, 0); e != nil {
					writeErr <- e
					return
				}
			}
		}
	}()
	for n := 0; n < 40; n++ {
		dst, e := os.CreateTemp(root, "clone-")
		if e != nil {
			t.Fatal(e)
		}
		if e = unix.IoctlFileClone(int(dst.Fd()), int(fd.Fd())); e != nil {
			t.Fatal(e)
		}
		if _, e = dst.Seek(0, 0); e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(dst)
		dst.Close()
		os.Remove(dst.Name())
		if e != nil || (!bytes.Equal(data, a) && !bytes.Equal(data, b)) {
			t.Fatalf("mixed clone at %d: length %d, error %v", n, len(data), e)
		}
	}
	finish()
	select {
	case e := <-writeErr:
		t.Fatal(e)
	default:
	}
	// Drive the real domain adapter with mandatory cloning, not an io.Copy fallback.
	catalog := filepath.Join(t.TempDir(), "shares.db")
	content := filepath.Join(t.TempDir(), "content")
	store, e := shares.Open(catalog, content, shares.Options{Backend: NewBackend(f.remote), RequireClone: true, Active: func(context.Context, string) (bool, error) { return true, nil }})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	v, e := store.Create(f.ac, user(f.alice), shares.CreateRequest{RequestID: "capture", SourceIDs: []string{entry.ID}, RecipientIDs: []string{f.bob.ID}})
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, e = store.Get(f.ac, user(f.alice), v.ID, false)
		if e != nil {
			t.Fatal(e)
		}
		if v.Status != "preparing" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if v.Status != "published" || len(v.Entries) != 1 {
		t.Fatalf("mandatory clone not published: %+v", v)
	}
	original, e := os.ReadFile(source)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(source, source+".moved"); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(source + ".moved"); e != nil {
		t.Fatal(e)
	}
	r, _, e := store.OpenContent(f.bc, user(f.bob), v.ID, v.Entries[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(r)
	r.Close()
	if e != nil || !bytes.Equal(data, original) {
		t.Fatal("published copy changed with source")
	}
}
