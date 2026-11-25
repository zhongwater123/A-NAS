// Package unixmsg frames JSON messages on Unix stream sockets: a four-byte
// big-endian length, then the JSON body, with file descriptors passed as
// SCM_RIGHTS beside the length. Read and Write carry at most one; ReadFiles
// and WriteFiles carry as many as the receiver allows. The file broker, the
// media worker, the sharing bridge and the AI worker speak it; each sets its
// own size limit (#222).
package unixmsg

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
)

const headerSize = 4

var (
	// ErrTooLarge reports a message over the limit, sent or received.
	ErrTooLarge = errors.New("message is too large")
	// ErrTruncated reports descriptors the kernel dropped because the
	// receiver had no room for them.
	ErrTruncated = errors.New("message descriptors were truncated")
	// ErrTooManyFiles reports a message with more descriptors than the
	// receiver accepts.
	ErrTooManyFiles = errors.New("message has too many descriptors")
)

// Encode marshals value, refusing a body over limit bytes.
func Encode(value any, limit int) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, ErrTooLarge
	}
	return body, nil
}

// Write sends value, refusing a body over limit bytes. A file, when given,
// travels with the length.
func Write(conn *net.UnixConn, value any, file *os.File, limit int) error {
	body, err := Encode(value, limit)
	if err != nil {
		return err
	}
	return WriteBody(conn, body, file)
}

// WriteBody sends a body that Encode returned. The length and the
// descriptor go in one sendmsg and the body in a write: the race detector
// sees a write on one side and a read on the other as ordered, but not
// sendmsg and recvmsg, so a reply sent whole by sendmsg hid that the
// sender's work came before the reader's.
func WriteBody(conn *net.UnixConn, body []byte, file *os.File) error {
	if file == nil {
		return WriteBodyFiles(conn, body, nil)
	}
	return WriteBodyFiles(conn, body, []*os.File{file})
}

// WriteFiles sends value with files, in order, refusing a body over limit
// bytes.
func WriteFiles(conn *net.UnixConn, value any, files []*os.File, limit int) error {
	body, err := Encode(value, limit)
	if err != nil {
		return err
	}
	return WriteBodyFiles(conn, body, files)
}

// WriteBodyFiles sends a body that Encode returned with files, in order.
func WriteBodyFiles(conn *net.UnixConn, body []byte, files []*os.File) error {
	header := binary.BigEndian.AppendUint32(make([]byte, 0, headerSize), uint32(len(body)))
	var rights []byte
	if len(files) > 0 {
		descriptors := make([]int, len(files))
		for i, file := range files {
			descriptors[i] = int(file.Fd())
		}
		rights = syscall.UnixRights(descriptors...)
	}
	written, _, err := conn.WriteMsgUnix(header, rights, nil)
	if err == nil && written < len(header) {
		_, err = conn.Write(header[written:])
	}
	if err == nil {
		_, err = conn.Write(body)
	}
	return err
}

// Read reads one message into value and returns the descriptor that came
// with it, or nil. Descriptors beyond the first are closed, and a body over
// limit bytes is refused before it is read.
func Read(conn *net.UnixConn, value any, limit int) (*os.File, error) {
	body, file, err := ReadBody(conn, limit)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, value); err != nil {
		CloseFile(file)
		return nil, fmt.Errorf("decode message: %w", err)
	}
	return file, nil
}

// ReadBody reads one message's body and descriptor without decoding it.
// Descriptors beyond the first are closed.
func ReadBody(conn *net.UnixConn, limit int) ([]byte, *os.File, error) {
	// Room for four keeps a sender of a few extra descriptors from
	// truncating the message; only the first is kept.
	body, files, err := readBody(conn, limit, 4)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return body, nil, nil
	}
	CloseFiles(files[1:])
	return body, files[0], nil
}

// ReadFiles reads one message into value and returns the descriptors that
// came with it, in order. A message with more than most descriptors is
// refused, and a body over limit bytes is refused before it is read.
func ReadFiles(conn *net.UnixConn, value any, limit, most int) ([]*os.File, error) {
	body, files, err := ReadBodyFiles(conn, limit, most)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, value); err != nil {
		CloseFiles(files)
		return nil, fmt.Errorf("decode message: %w", err)
	}
	return files, nil
}

// ReadBodyFiles reads one message's body and up to most descriptors without
// decoding it.
func ReadBodyFiles(conn *net.UnixConn, limit, most int) ([]byte, []*os.File, error) {
	if most < 1 {
		most = 1
	}
	body, files, err := readBody(conn, limit, most)
	switch {
	case errors.Is(err, ErrTruncated):
		// The kernel had no room for the rest.
		return nil, nil, ErrTooManyFiles
	case err == nil && len(files) > most:
		CloseFiles(files)
		return nil, nil, ErrTooManyFiles
	}
	return body, files, err
}

// readBody reads a message with room for room descriptors. The kernel may
// fit a few more into the room's padding; callers check the count.
//
// A message whose descriptors the kernel dropped is still read to its end
// before it is refused. The sender writes the body after the descriptors; a
// receiver that answered and closed without reading it would fail that
// write, and the sender would report the receiver gone instead of reading
// the refusal.
func readBody(conn *net.UnixConn, limit, room int) ([]byte, []*os.File, error) {
	header := make([]byte, headerSize)
	rights := make([]byte, syscall.CmsgSpace(room*4))
	count, rightsCount, flags, _, err := conn.ReadMsgUnix(header, rights)
	// A connection closed or cancelled under the read reports -1, not 0.
	if count <= 0 && err != nil {
		return nil, nil, err
	}
	files, err := receivedFiles(rights[:rightsCount])
	if err == nil && count < headerSize {
		_, err = io.ReadFull(conn, header[count:])
	}
	var body []byte
	if err == nil {
		length := binary.BigEndian.Uint32(header)
		if uint64(length) > uint64(limit) {
			err = ErrTooLarge
		} else {
			body = make([]byte, length)
			_, err = io.ReadFull(conn, body)
		}
	}
	if err == nil && flags&syscall.MSG_CTRUNC != 0 {
		err = ErrTruncated
	}
	if err != nil {
		CloseFiles(files)
		return nil, nil, err
	}
	return body, files, nil
}

// receivedFiles opens the descriptors of a message, in order.
func receivedFiles(rights []byte) ([]*os.File, error) {
	if len(rights) == 0 {
		return nil, nil
	}
	messages, err := syscall.ParseSocketControlMessage(rights)
	if err != nil {
		return nil, err
	}
	var descriptors []int
	for _, message := range messages {
		received, err := syscall.ParseUnixRights(&message)
		if err != nil {
			for _, descriptor := range descriptors {
				_ = syscall.Close(descriptor)
			}
			return nil, err
		}
		descriptors = append(descriptors, received...)
	}
	files := make([]*os.File, 0, len(descriptors))
	for _, descriptor := range descriptors {
		syscall.CloseOnExec(descriptor)
		files = append(files, os.NewFile(uintptr(descriptor), "unixmsg"))
	}
	return files, nil
}

// CloseFile closes a received or sent file, if any.
func CloseFile(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}

// CloseFiles closes received or sent files.
func CloseFiles(files []*os.File) {
	for _, file := range files {
		CloseFile(file)
	}
}
