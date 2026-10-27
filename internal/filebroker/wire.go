// Package filebroker runs Web file operations as the signed-in user (ADR 0008).
//
// The root File Broker inside the Host Agent verifies each request's session
// token against the session store, then forwards the operation to a worker
// process running with that user's UID and groups. The kernel's POSIX ACLs
// decide every access. Opened and created files travel back as file
// descriptors over SCM_RIGHTS, so root reads and writes no file contents,
// except for the assistant's changes: their executor (internal/assistantchanges)
// copies a staged file through such descriptors, a block at a time, into its
// own store when the card is made and from there into place once approved.
package filebroker

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

// maxMessage bounds one JSON message. A scan travels in pages far below it;
// a reply that still does not fit is answered with an error instead. Test
// workers lower it.
var maxMessage = 64 << 20

// scanPage bounds the entries in one message of a scan, so neither the
// broker nor a worker ever holds a whole large tree in one message.
var scanPage = 2048

var errTooLarge = errors.New("file broker message is too large")

// encodeMessage encodes value, refusing a message over the limit.
func encodeMessage(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(body) > maxMessage {
		return nil, errTooLarge
	}
	return body, nil
}

// writeMessage sends a length-prefixed JSON message. A file, when given,
// travels as SCM_RIGHTS ancillary data attached to the length prefix.
func writeMessage(conn *net.UnixConn, value any, file *os.File) error {
	body, err := encodeMessage(value)
	if err != nil {
		return err
	}
	return writeEncoded(conn, body, file)
}

func writeEncoded(conn *net.UnixConn, body []byte, file *os.File) error {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(body)))
	var rights []byte
	if file != nil {
		rights = syscall.UnixRights(int(file.Fd()))
	}
	if _, _, err := conn.WriteMsgUnix(header, rights, nil); err != nil {
		return err
	}
	_, err := conn.Write(body)
	return err
}

// readMessage reads one message and the file descriptor attached to it, if
// any. Extra descriptors are closed.
func readMessage(conn *net.UnixConn, value any) (*os.File, error) {
	header := make([]byte, 4)
	rights := make([]byte, syscall.CmsgSpace(4*4))
	count, rightsCount, _, _, err := conn.ReadMsgUnix(header, rights)
	// A connection closed under the read reports -1, not 0.
	if count <= 0 && err != nil {
		return nil, err
	}
	file, rightsErr := receivedFile(rights[:rightsCount])
	if count < len(header) {
		if _, err := io.ReadFull(conn, header[count:]); err != nil {
			closeFile(file)
			return nil, err
		}
	}
	if rightsErr != nil {
		closeFile(file)
		return nil, rightsErr
	}
	length := binary.BigEndian.Uint32(header)
	if length > uint32(maxMessage) {
		closeFile(file)
		return nil, errTooLarge
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		closeFile(file)
		return nil, err
	}
	if err := json.Unmarshal(body, value); err != nil {
		closeFile(file)
		return nil, fmt.Errorf("decode file broker message: %w", err)
	}
	return file, nil
}

// readReply reads a whole reply: the pages of a scan, then its final
// message, which alone may carry a file.
func readReply(conn *net.UnixConn) (response, *os.File, error) {
	var entries pages
	for {
		var result response
		file, err := readMessage(conn, &result)
		if err != nil {
			return response{}, nil, err
		}
		if !result.More {
			return entries.complete(result), file, nil
		}
		closeFile(file)
		entries.add(result)
	}
}

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
	return os.NewFile(uintptr(descriptors[0]), "a-nas-file"), nil
}

func closeFile(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}
