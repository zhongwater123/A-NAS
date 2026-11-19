package unixmsg

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type note struct {
	Text string `json:"text"`
}

func TestAMessageCarriesItsDescriptor(t *testing.T) {
	t.Parallel()
	left, right := pair(t)
	path := filepath.Join(t.TempDir(), "passed")
	if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer CloseFile(file)
	if err := Write(left, note{Text: "hello"}, file, 1024); err != nil {
		t.Fatal(err)
	}
	if err := Write(left, note{Text: "again"}, nil, 1024); err != nil {
		t.Fatal(err)
	}
	var first, second note
	received, err := Read(right, &first, 1024)
	if err != nil || first.Text != "hello" || received == nil {
		t.Fatalf("Read() = %v, %+v, %v", received, first, err)
	}
	defer CloseFile(received)
	if data, err := io.ReadAll(received); err != nil || string(data) != "contents" {
		t.Fatalf("passed file reads %q, %v", data, err)
	}
	if received, err := Read(right, &second, 1024); err != nil || second.Text != "again" || received != nil {
		t.Fatalf("Read() = %v, %+v, %v", received, second, err)
	}
}

func TestAMessageCarriesSeveralDescriptorsInOrder(t *testing.T) {
	t.Parallel()
	left, right := pair(t)
	dir := t.TempDir()
	var files []*os.File
	for _, name := range []string{"video", "song", "spare"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer CloseFile(file)
		files = append(files, file)
	}
	if err := WriteFiles(left, note{Text: "two"}, files[:2], 1024); err != nil {
		t.Fatal(err)
	}
	var got note
	received, err := ReadFiles(right, &got, 1024, 2)
	if err != nil || got.Text != "two" || len(received) != 2 {
		t.Fatalf("ReadFiles() = %v, %+v, %v", received, got, err)
	}
	defer CloseFiles(received)
	for i, want := range []string{"video", "song"} {
		if data, err := io.ReadAll(received[i]); err != nil || string(data) != want {
			t.Fatalf("descriptor %d reads %q, %v", i, data, err)
		}
	}

	// More than the receiver takes is refused, and none of them is kept.
	if err := WriteFiles(left, note{Text: "three"}, files, 1024); err != nil {
		t.Fatal(err)
	}
	if received, err := ReadFiles(right, &got, 1024, 2); !errors.Is(err, ErrTooManyFiles) || received != nil {
		t.Fatalf("ReadFiles() of three descriptors = %v, %v", received, err)
	}
	// A refused message is not read to its end; the connection is done.
	left, right = pair(t)
	// Read keeps the first of several, as before. (A passed descriptor
	// shares its offset: the two read above are at their end.)
	if err := WriteFiles(left, note{Text: "first"}, []*os.File{files[2], files[0]}, 1024); err != nil {
		t.Fatal(err)
	}
	file, err := Read(right, &got, 1024)
	if err != nil || file == nil {
		t.Fatalf("Read() = %v, %v", file, err)
	}
	defer CloseFile(file)
	if data, _ := io.ReadAll(file); string(data) != "spare" {
		t.Fatalf("Read() kept %q", data)
	}
}

func TestMessagesOverTheLimitAreRefused(t *testing.T) {
	t.Parallel()
	left, right := pair(t)
	if err := Write(left, note{Text: "too long"}, nil, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Write() over the limit error = %v", err)
	}
	if err := Write(left, note{Text: "long enough"}, nil, 1024); err != nil {
		t.Fatal(err)
	}
	var got note
	if _, err := Read(right, &got, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Read() over the limit error = %v", err)
	}
}

// FuzzRead feeds Read whatever arrives on a socket: it must return a message
// or an error, never panic, and never read past its limit.
func FuzzRead(f *testing.F) {
	frame := func(body string) []byte {
		return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
	}
	f.Add(frame(`{"text":"hello"}`))
	f.Add(frame(`{"text":`))
	f.Add(frame(`[1,2,3]`))
	f.Add([]byte{0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, '{'})
	f.Add(append(frame(`{}`), frame(`{"text":"next"}`)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		left, right := pair(t)
		go func() {
			_, _ = left.Write(data)
			_ = left.CloseWrite()
		}()
		for i := range 4 {
			var got note
			if i%2 == 1 {
				files, err := ReadFiles(right, &got, 64, 2)
				CloseFiles(files)
				if err != nil {
					return
				}
				continue
			}
			file, err := Read(right, &got, 64)
			CloseFile(file)
			if err != nil {
				return
			}
		}
	})
}

func pair(t testing.TB) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var conns [2]*net.UnixConn
	for i, fd := range fds {
		file := os.NewFile(uintptr(fd), "pair")
		connection, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = connection.(*net.UnixConn)
		t.Cleanup(func() { _ = conns[i].Close() })
	}
	return conns[0], conns[1]
}
