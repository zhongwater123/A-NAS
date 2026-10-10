package mediaworker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Output limits for buffered operations.
var maxOutput = map[string]int64{OpProbe: 4 << 20, OpFrame: 8 << 20, OpSubtitle: 32 << 20}

// timeouts bound one operation; a stream ends when either side closes.
var timeouts = map[string]time.Duration{OpProbe: time.Minute, OpFrame: time.Minute, OpSubtitle: 15 * time.Minute, OpStream: 8 * time.Hour}

// Serve answers one request on conn and returns when it is done.
func Serve(ctx context.Context, conn *net.UnixConn, tools Tools) error {
	defer conn.Close()
	var request Request
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	file, err := readFrame(conn, &request)
	if err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Time{})
	if file == nil {
		return writeFrame(conn, Response{Code: CodeInvalid, Message: "no file descriptor"}, nil)
	}
	defer file.Close()
	if err := request.Validate(); err != nil {
		return writeFrame(conn, Response{Code: CodeInvalid, Message: err.Error()}, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, timeouts[request.Op])
	defer cancel()
	program, args := tools.command(request)
	command := exec.CommandContext(ctx, program, args...)
	command.Stdin = file
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C.UTF-8"}
	var stderr limitedBuffer
	stderr.limit = 8 << 10
	command.Stderr = &stderr
	if request.Op == OpStream {
		return serveStream(ctx, conn, command, &stderr)
	}
	var stdout limitedBuffer
	stdout.limit = maxOutput[request.Op]
	command.Stdout = &stdout
	if err := command.Run(); err != nil || stdout.overflow || stdout.Len() == 0 {
		return writeFrame(conn, failure(err, &stderr, stdout.overflow), nil)
	}
	if err := writeFrame(conn, Response{OK: true}, nil); err != nil {
		return err
	}
	_, err = conn.Write(stdout.Bytes())
	return err
}

func failure(err error, stderr *limitedBuffer, overflow bool) Response {
	message := strings.TrimSpace(stderr.String())
	switch {
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
		return writeFrame(conn, Response{Code: CodeFailed, Message: err.Error()}, nil)
	}
	buffer := make([]byte, 64<<10)
	count, _ := io.ReadAtLeast(stdout, buffer, 1)
	if count == 0 {
		return writeFrame(conn, failure(command.Wait(), stderr, false), nil)
	}
	if err := writeFrame(conn, Response{OK: true}, nil); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	// The peer never writes after its request: a read that returns means it
	// has gone.
	go func() {
		_, _ = conn.Read(make([]byte, 1))
		_ = command.Process.Kill()
	}()
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
