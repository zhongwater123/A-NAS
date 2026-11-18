package ffmpegworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// Output limits for buffered operations.
var maxOutput = map[string]int64{OpProbe: 4 << 20, OpFrame: 8 << 20, OpImage: 12 << 20, OpSubtitle: 32 << 20}

// timeouts bound one operation; a stream ends when either side closes.
var timeouts = map[string]time.Duration{OpProbe: time.Minute, OpFrame: time.Minute, OpImage: 30 * time.Second, OpSubtitle: 15 * time.Minute, OpStream: 8 * time.Hour, OpAudio: 8 * time.Hour}

// Serve answers one request on conn and returns when it is done.
func Serve(ctx context.Context, conn *net.UnixConn, tools Tools) error {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	body, files, err := unixmsg.ReadBodyFiles(conn, maxFrame, maxFiles)
	if err != nil {
		return err
	}
	defer unixmsg.CloseFiles(files)
	_ = conn.SetReadDeadline(time.Time{})
	// Every request names its operation and may ask for an acknowledgement;
	// the editing operations decode the rest strictly.
	var head struct {
		Op  string `json:"op"`
		Ack bool   `json:"ack"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return fmt.Errorf("decode message: %w", err)
	}
	if len(files) == 0 {
		return writeFrame(conn, Response{Code: CodeInvalid, Message: "no file descriptor"}, nil)
	}
	if head.Ack {
		if err := writeFrame(conn, Response{Accepted: true}, nil); err != nil {
			return err
		}
	}
	// A descriptor shares its offset with the sender's, which an earlier
	// operation on the same file may have moved; fd: reads from the offset.
	for _, file := range files {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return writeFrame(conn, Response{Code: CodeUnreadable, Message: "the file cannot seek"}, nil)
		}
	}
	if isEditing(head.Op) {
		return serveEditing(ctx, conn, tools, head.Op, body, files)
	}
	var request Request
	if err := json.Unmarshal(body, &request); err != nil {
		return fmt.Errorf("decode message: %w", err)
	}
	if request.Op == OpAudio && request.PCM {
		if err := decodeStrict(body, &request); err != nil {
			return writeFrame(conn, Response{Code: CodeInvalid, Message: err.Error()}, nil)
		}
	}
	file := files[0]
	if err := request.Validate(); err != nil {
		return writeFrame(conn, Response{Code: CodeInvalid, Message: err.Error()}, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, timeouts[request.Op])
	defer cancel()
	_ = conn.SetWriteDeadline(time.Time{})
	program, args := tools.command(request)
	if program == "" {
		return writeFrame(conn, Response{Code: CodeUnavailable, Message: "decoder is unavailable"}, nil)
	}
	stdin := file
	if request.Op == OpImage {
		// libheif opens a filename instead of consuming an existing descriptor.
		// Reopening the caller's /proc/self/fd/0 would fail under DynamicUser.
		// Copy into a bounded, worker-owned anonymous file, never onto disk.
		input, err := imageInput(file)
		if err != nil {
			return writeFrame(conn, Response{Code: CodeUnreadable, Message: "image input is unreadable or too large"}, nil)
		}
		defer func() { _ = input.Close() }()
		stdin = input
	}
	command, stderr := newCommand(ctx, program, args, request.PCM && request.Op == OpAudio)
	command.Stdin = stdin
	// The peer sends nothing beyond its request. Watch even before the
	// decoder produces its first bytes.
	go func() { _, _ = conn.Read(make([]byte, 1)); cancel() }()
	if request.Op == OpStream || request.Op == OpAudio {
		return serveStream(ctx, conn, command, stderr)
	}
	var stdout limitedBuffer
	stdout.limit = maxOutput[request.Op]
	command.Stdout = &stdout
	if err := command.Run(); err != nil || stdout.overflow || stdout.Len() == 0 {
		return writeFrame(conn, failure(err, stderr, stdout.overflow), nil)
	}
	if err := writeFrame(conn, Response{OK: true}, nil); err != nil {
		return err
	}
	_, err = conn.Write(stdout.Bytes())
	return err
}

func imageInput(source *os.File) (*os.File, error) {
	const limit = 256 << 20
	info, err := source.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrUnreadable
	}
	fd, err := unix.MemfdCreate("anas-image", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	input := os.NewFile(uintptr(fd), "anas-image")
	n, err := io.Copy(input, io.LimitReader(source, limit+1))
	if err == nil && n > limit {
		err = ErrUnreadable
	}
	if err == nil {
		_, err = input.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	return input, nil
}

func failure(err error, stderr *limitedBuffer, overflow bool) Response {
	message := strings.TrimSpace(stderr.String())
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		return Response{Code: CodeUnavailable, Message: "decoder is unavailable"}
	case overflow:
		message = "output is too large"
	case errors.Is(err, context.DeadlineExceeded):
		return Response{Code: CodeFailed, Message: "timed out"}
	case message == "" && err != nil:
		message = err.Error()
	case message == "":
		message = "no output"
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) || overflow || err == nil {
		return Response{Code: CodeUnreadable, Message: lastLine(message)}
	}
	return Response{Code: CodeFailed, Message: lastLine(message)}
}

func lastLine(message string) string {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// serveStream answers as soon as FFmpeg produces output, then copies it to
// the connection. A peer that hangs up ends FFmpeg.
func serveStream(ctx context.Context, conn *net.UnixConn, command *exec.Cmd, stderr *limitedBuffer) error {
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return writeFrame(conn, failure(err, stderr, false), nil)
	}
	buffer := make([]byte, 64<<10)
	count, _ := io.ReadAtLeast(stdout, buffer, 1)
	if count == 0 {
		err := command.Wait()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return writeFrame(conn, failure(err, stderr, false), nil)
	}
	if err := writeFrame(conn, Response{OK: true}, nil); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	_, err = conn.Write(buffer[:count])
	if err == nil {
		_, err = io.CopyBuffer(conn, stdout, buffer)
	}
	if err != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if err == nil && waitErr != nil && ctx.Err() == nil {
		err = waitErr
	}
	return err
}

// limitedBuffer keeps at most limit bytes and remembers whether more came.
type limitedBuffer struct {
	bytes.Buffer
	limit    int64
	overflow bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	room := b.limit - int64(b.Len())
	if int64(len(data)) > room {
		b.overflow = true
		if room > 0 {
			b.Buffer.Write(data[:room])
		}
		return len(data), nil
	}
	return b.Buffer.Write(data)
}

// ServeStandardInput serves the connection systemd passes as standard input
// (StandardInput=socket with Accept=yes).
func ServeStandardInput(ctx context.Context, tools Tools) error {
	conn, err := net.FileConn(os.Stdin)
	if err != nil {
		return err
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return errors.New("standard input is not a Unix socket")
	}
	return Serve(ctx, unixConn, tools)
}
