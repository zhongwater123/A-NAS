// Package filebroker runs Web file operations as the signed-in user (ADR 0008).
//
// The root File Broker inside the Host Agent verifies each request's session
// token against the session store, then forwards the operation to a worker
// process running with that user's UID and groups. The kernel's POSIX ACLs
// decide every access. Opened and created files travel back as file
// descriptors over SCM_RIGHTS, so root never reads or writes file contents.
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

// maxMessage bounds one JSON message; a scan of a very large tree is the
// biggest legitimate response.
const maxMessage = 64 << 20

// writeMessage sends a length-prefixed JSON message. A file, when given,
// travels as SCM_RIGHTS ancillary data attached to the length prefix.
func writeMessage(conn *net.UnixConn, value any, file *os.File) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > maxMessage {
		return errors.New("file broker message is too large")
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(body)))
	var rights []byte
	if file != nil {
		rights = syscall.UnixRights(int(file.Fd()))
	}
	if _, _, err := conn.WriteMsgUnix(header, rights, nil); err != nil {
		return err
	}
	_, err = conn.Write(body)
	return err
}

// readMessage reads one message and the file descriptor attached to it, if
// any. Extra descriptors are closed.
func readMessage(conn *net.UnixConn, value any) (*os.File, error) {
	header := make([]byte, 4)
	rights := make([]byte, syscall.CmsgSpace(4*4))
	count, rightsCount, _, _, err := conn.ReadMsgUnix(header, rights)
	if count == 0 && err != nil {
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
	if length > maxMessage {
		closeFile(file)
		return nil, errors.New("file broker message is too large")
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
