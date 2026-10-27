// Package unixmsg frames JSON messages on Unix stream sockets: a four-byte
// big-endian length, then the JSON body, with at most one file descriptor
// passed as SCM_RIGHTS beside the length. The file broker, the media worker,
// the sharing bridge and the AI worker speak it; each sets its own size limit
// (#222).
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

// WriteBody sends a body that Encode returned, in one sendmsg when the
// socket takes it whole.
func WriteBody(conn *net.UnixConn, body []byte, file *os.File) error {
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, headerSize+len(body)), uint32(len(body)))
	frame = append(frame, body...)
	var rights []byte
	if file != nil {
		rights = syscall.UnixRights(int(file.Fd()))
	}
	written, _, err := conn.WriteMsgUnix(frame, rights, nil)
	if err == nil && written < len(frame) {
		_, err = conn.Write(frame[written:])
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
func ReadBody(conn *net.UnixConn, limit int) ([]byte, *os.File, error) {
	header := make([]byte, headerSize)
	rights := make([]byte, syscall.CmsgSpace(4*4))
	count, rightsCount, flags, _, err := conn.ReadMsgUnix(header, rights)
	// A connection closed or cancelled under the read reports -1, not 0.
	if count <= 0 && err != nil {
		return nil, nil, err
	}
	file, err := receivedFile(rights[:rightsCount])
	if err == nil && flags&syscall.MSG_CTRUNC != 0 {
		err = ErrTruncated
	}
	if err == nil && count < headerSize {
		_, err = io.ReadFull(conn, header[count:])
	}
	if err != nil {
		CloseFile(file)
		return nil, nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if uint64(length) > uint64(limit) {
		CloseFile(file)
		return nil, nil, ErrTooLarge
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		CloseFile(file)
		return nil, nil, err
	}
	return body, file, nil
}

// receivedFile keeps the first descriptor of a message and closes the rest.
func receivedFile(rights []byte) (*os.File, error) {
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
	if len(descriptors) == 0 {
		return nil, nil
	}
	for _, extra := range descriptors[1:] {
		_ = syscall.Close(extra)
	}
	syscall.CloseOnExec(descriptors[0])
	return os.NewFile(uintptr(descriptors[0]), "unixmsg"), nil
}

// CloseFile closes a received or sent file, if any.
func CloseFile(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}
