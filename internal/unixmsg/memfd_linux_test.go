package unixmsg

import (
	"errors"
	"io"
	"testing"

	"golang.org/x/sys/unix"
)

// A sealed file reaches FFmpeg and the Workers read-only (ADR 0017): its
// descriptor cannot write, is closed on exec, carries every seal, and reads
// the whole payload from its start.
func TestSealedFileIsReadOnly(t *testing.T) {
	t.Parallel()
	file, err := SealedFile("a-nas-test", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		t.Fatalf("access mode = %#o, %v; want read-only", flags&unix.O_ACCMODE, err)
	}
	if fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0); err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("descriptor flags = %#x, %v; want FD_CLOEXEC", fdFlags, err)
	}
	if _, err := file.Write([]byte("x")); !errors.Is(err, unix.EBADF) {
		t.Fatalf("Write() = %v, want EBADF", err)
	}
	want := unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE | unix.F_SEAL_SEAL
	if seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0); err != nil || seals&want != want {
		t.Fatalf("seals = %#x, %v; want %#x", seals, err, want)
	}
	content, err := io.ReadAll(file)
	if err != nil || string(content) != "payload" {
		t.Fatalf("content = %q, %v", content, err)
	}
}

// A payload that fails to be written leaves no file behind for the caller.
func TestSealedFileReportsTheWritersError(t *testing.T) {
	t.Parallel()
	failure := errors.New("source gone")
	file, err := WriteSealedFile("a-nas-test", func(w io.Writer) error {
		_, _ = w.Write([]byte("part"))
		return failure
	})
	if file != nil || !errors.Is(err, failure) {
		t.Fatalf("WriteSealedFile() = %v, %v; want the writer's error", file, err)
	}
}
