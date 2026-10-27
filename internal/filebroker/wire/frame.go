package wire

import (
	"net"
	"os"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// MaxMessage bounds one JSON message. A scan travels in pages far below it;
// a reply that still does not fit is answered with an error instead. Test
// workers lower it.
var MaxMessage = 64 << 20

// ScanPage bounds the entries in one message of a scan, so neither the
// broker nor a worker ever holds a whole large tree in one message.
var ScanPage = 2048

// ErrTooLarge reports a message over MaxMessage.
var ErrTooLarge = unixmsg.ErrTooLarge

// EncodeMessage encodes value, refusing a message over the limit.
func EncodeMessage(value any) ([]byte, error) { return unixmsg.Encode(value, MaxMessage) }

// WriteMessage sends a message (internal/unixmsg). A file, when given,
// travels with it.
func WriteMessage(conn *net.UnixConn, value any, file *os.File) error {
	return unixmsg.Write(conn, value, file, MaxMessage)
}

// WriteEncoded sends a message that EncodeMessage returned.
func WriteEncoded(conn *net.UnixConn, body []byte, file *os.File) error {
	return unixmsg.WriteBody(conn, body, file)
}

// ReadMessage reads one message and the file descriptor attached to it, if
// any.
func ReadMessage(conn *net.UnixConn, value any) (*os.File, error) {
	return unixmsg.Read(conn, value, MaxMessage)
}

// ReadReply reads a whole reply: the pages of a scan, then its final
// message, which alone may carry a file.
func ReadReply(conn *net.UnixConn) (Response, *os.File, error) {
	var entries Pages
	for {
		var result Response
		file, err := ReadMessage(conn, &result)
		if err != nil {
			return Response{}, nil, err
		}
		if !result.More {
			return entries.Complete(result), file, nil
		}
		CloseFile(file)
		entries.Add(result)
	}
}

// CloseFile closes a file of a message, if any.
func CloseFile(file *os.File) { unixmsg.CloseFile(file) }
