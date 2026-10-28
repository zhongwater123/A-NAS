package wire

import (
	"encoding/binary"
	"net"
	"os"
	"syscall"
	"testing"
)

// FuzzReadMessage is what root reads from the Product Service: whatever
// arrives, ReadMessage returns a request or an error, never panics (#223).
func FuzzReadMessage(f *testing.F) {
	frame := func(body string) []byte {
		return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
	}
	f.Add(frame(`{"op":"scan","path":"/srv/a","skip":{"names":["x"],"limit":3},"paged":true}`))
	f.Add(frame(`{"op":"change_prepare","change":{"arguments":{"operations":[]}}}`))
	f.Add(frame(`{"op":"set_mod_time","modTime":"not a time"}`))
	f.Add([]byte{0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
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
			defer func() { _ = conns[i].Close() }()
		}
		go func() {
			_, _ = conns[0].Write(data)
			_ = conns[0].CloseWrite()
		}()
		var req Request
		file, err := ReadMessage(conns[1], &req)
		CloseFile(file)
		if err == nil {
			_ = IsFileOp(req.Op)
		}
	})
}
