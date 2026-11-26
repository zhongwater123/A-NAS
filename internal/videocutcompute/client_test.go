//go:build linux

package videocutcompute_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// request is what a stand-in Worker read: the request's JSON and each
// array's bytes.
type request struct {
	fields map[string]any
	arrays [][]byte
}

// standIn listens as the Worker does and answers each connection with
// answer, which gets the request, or nil when it could not be read.
func standIn(t *testing.T, answer func(conn *net.UnixConn, got *request)) videocutcompute.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "compute")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "compute.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			answer(conn, read(conn))
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return videocutcompute.Client{SocketPath: socket}
}

func read(conn *net.UnixConn) *request {
	body, files, err := unixmsg.ReadBodyFiles(conn, 1<<20, 8)
	if err != nil {
		return nil
	}
	defer unixmsg.CloseFiles(files)
	got := &request{}
	if err := json.Unmarshal(body, &got.fields); err != nil {
		return nil
	}
	for _, file := range files {
		content, err := io.ReadAll(file)
		if err != nil {
			return nil
		}
		got.arrays = append(got.arrays, content)
	}
	return got
}

// replying answers every request with body.
func replying(body string) func(*net.UnixConn, *request) {
	return func(conn *net.UnixConn, _ *request) { _ = unixmsg.WriteBody(conn, []byte(body), nil) }
}

func hungUp(*net.UnixConn, *request) {}

// only fails unless err is want and neither of the other two: the photo
// library waits for an unavailable Worker without counting, counts a failed
// call as an attempt, and gives up on a refused one.
func only(t *testing.T, what string, err, want error) {
	t.Helper()
	for _, kind := range []error{videocutcompute.ErrUnavailable, videocutcompute.ErrRefused, videocutcompute.ErrFailed} {
		if errors.Is(err, kind) != errors.Is(want, kind) {
			t.Fatalf("%s = %v, want only %v", what, err, want)
		}
	}
}

func TestACallSendsItsArraysAndDecodesTheAnswer(t *testing.T) {
	requests := make(chan *request, 1)
	client := standIn(t, func(conn *net.UnixConn, read *request) {
		requests <- read
		_ = unixmsg.WriteBody(conn, []byte(`{"ok": true, "points": 12.5}`), nil)
	})
	points, err := client.SharpPoints(context.Background(), []byte{1, 2, 3, 4, 5, 6}, 3, 2)
	if err != nil || points != 12.5 {
		t.Fatalf("SharpPoints() = %v, %v", points, err)
	}
	got := <-requests
	if got == nil || got.fields["op"] != "quality_part" || got.fields["kind"] != "sharp" ||
		len(got.arrays) != 1 || string(got.arrays[0]) != "\x01\x02\x03\x04\x05\x06" {
		t.Fatalf("the Worker read %+v", got)
	}
	spec, _ := json.Marshal(got.fields["arrays"])
	if string(spec) != `[{"dtype":"uint8","name":"frame","shape":[2,3]}]` {
		t.Fatalf("arrays = %s", spec)
	}
}

// What went wrong decides what becomes of the photo library's job
// (photos.finishMontageError): only a Worker that cannot be reached leaves
// the attempt unspent.
func TestFailuresAreToldApart(t *testing.T) {
	ctx := context.Background()
	blur := 3.0
	input := videocutcompute.QualityInput{Moves: [][2]float64{}, Blurs: []*float64{&blur}, Blocked: []float64{0}, Points: []*float64{nil}}

	dir, err := os.MkdirTemp("", "compute")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	away := videocutcompute.Client{SocketPath: filepath.Join(dir, "none.sock")}

	t.Run("no socket", func(t *testing.T) {
		_, err := away.Quality(ctx, input)
		only(t, "a call without the socket", err, videocutcompute.ErrUnavailable)
	})
	t.Run("nobody listening", func(t *testing.T) {
		// The socket's file stays; nothing listens on it.
		socket := filepath.Join(dir, "closed.sock")
		closed, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		closed.SetUnlinkOnClose(false)
		_ = closed.Close()
		_, err = videocutcompute.Client{SocketPath: socket}.Quality(ctx, input)
		only(t, "a call nobody listens for", err, videocutcompute.ErrUnavailable)
	})
	for _, c := range []struct {
		name   string
		answer func(*net.UnixConn, *request)
		want   error
	}{
		{"an unexpected exception in the Worker", replying(`{"ok": false, "error": {"code": "internal", "message": "the operation failed"}}`), videocutcompute.ErrFailed},
		{"a request the Worker refuses", replying(`{"ok": false, "error": {"code": "invalid_input", "message": "a card's keyframes differ in number"}}`), videocutcompute.ErrRefused},
		{"a failure without its error", replying(`{"ok": false}`), videocutcompute.ErrFailed},
		{"an answer that is not JSON", replying(`not JSON`), videocutcompute.ErrFailed},
		{"an answer of another shape", replying(`{"ok": true, "blur": "sharp"}`), videocutcompute.ErrFailed},
		// Killed over its memory limit, or an answer over the frame limit
		// that it could not send: the connection just ends.
		{"a Worker that ends without answering", hungUp, videocutcompute.ErrFailed},
		{"an answer cut short", func(conn *net.UnixConn, _ *request) { _, _ = conn.Write([]byte{0, 0, 1, 0, '{'}) }, videocutcompute.ErrFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := standIn(t, c.answer).Quality(ctx, input)
			only(t, c.name, err, c.want)
		})
	}
	t.Run("a request over the frame limit", func(t *testing.T) {
		asked := make(chan bool, 1)
		client := standIn(t, func(_ *net.UnixConn, got *request) { asked <- got != nil })
		// Over 1 MiB of JSON: the same request would be too large again.
		large := input
		large.Moves = make([][2]float64, 300_000)
		_, err := client.Quality(ctx, large)
		only(t, "a request over the frame limit", err, videocutcompute.ErrRefused)
		if <-asked {
			t.Fatal("the Worker got a request over the frame limit")
		}
	})
	t.Run("arrays the request cannot pass", func(t *testing.T) {
		// Refused before the Worker is asked: there is none here.
		_, err := away.ShakeMoves(ctx, make([]byte, 10), 2)
		only(t, "frames of another size", err, videocutcompute.ErrRefused)
		err = away.Call(ctx, "cards", nil, make([]videocutcompute.Array, 9), nil)
		only(t, "nine arrays", err, videocutcompute.ErrRefused)
	})
}

// A caller that stops waiting gets its own context's error, which the photo
// library tells from a failure of the Worker: the job goes back unspent.
func TestACallTheCallerGaveUpOnIsNotAFailureOfTheWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	released := make(chan struct{})
	client := standIn(t, func(*net.UnixConn, *request) {
		cancel()
		<-released
	})
	defer close(released)
	done := make(chan error, 1)
	go func() {
		_, err := client.Keyframe(ctx, []byte("a keyframe"))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled call = %v", err)
		}
		only(t, "a cancelled call", err, nil)
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled call went on waiting for the Worker")
	}
}

// A member waits for Likeness (deleting a shot of a draft), so each failure
// is one the request's handler answers: a Worker that is away, or from
// before the operation existed, is waited for (503 compute_unavailable),
// not an error of the montage center's own.
func TestLikenessFailsAsARequestAMemberWaitsOn(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "compute")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	vector := []float32{1, 0, 0, 1}
	for _, c := range []struct {
		name   string
		client videocutcompute.Client
		want   error
	}{
		{"no Worker", videocutcompute.Client{SocketPath: filepath.Join(dir, "none.sock")}, app.ErrWorkerUnavailable},
		{"a Worker from before the operation", standIn(t, replying(`{"ok": false, "error": {"code": "invalid_input", "message": "unknown operation 'likeness'"}}`)), app.ErrWorkerUnavailable},
		{"a Worker that refuses the vectors", standIn(t, replying(`{"ok": false, "error": {"code": "invalid_input", "message": "a and b differ in dimension"}}`)), app.ErrWorkerRefused},
		{"a Worker that ends without answering", standIn(t, hungUp), app.ErrWorkerFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.client.Likeness(ctx, vector, vector, 2)
			for _, kind := range []error{app.ErrWorkerUnavailable, app.ErrWorkerRefused, app.ErrWorkerFailed} {
				if errors.Is(err, kind) != errors.Is(c.want, kind) {
					t.Fatalf("Likeness() = %v, want only %v", err, c.want)
				}
			}
		})
	}
	// Vectors that are not rows of the dimension are refused before any call.
	if _, err := (videocutcompute.Client{SocketPath: filepath.Join(dir, "none.sock")}).Likeness(ctx, vector[:3], vector, 2); !errors.Is(err, app.ErrWorkerRefused) {
		t.Fatalf("Likeness() of a row cut short = %v", err)
	}
	if alike, err := standIn(t, replying(`{"ok": true, "likeness": 0.5}`)).Likeness(ctx, vector, vector, 2); err != nil || alike != 0.5 {
		t.Fatalf("Likeness() = %v, %v", alike, err)
	}
}
