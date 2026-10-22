package mediaworker

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// slotDir creates a slot directory as the installer's tmpfiles entry does.
func slotDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for slot := range Slots {
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(slot)), nil, 0o440); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// impatient shortens the waits so that a busy worker shows within a test.
func impatient(t *testing.T) {
	t.Helper()
	oldPatience, oldPoll := patience, poll
	patience = [priorities]time.Duration{Interactive: 200 * time.Millisecond, Streaming: 200 * time.Millisecond}
	poll = [priorities]time.Duration{Interactive: 10 * time.Millisecond, Streaming: 10 * time.Millisecond, Background: 10 * time.Millisecond}
	t.Cleanup(func() { patience, poll = oldPatience, oldPoll })
}

// process is one process's view of a slot directory.
func process(dir string) *admission { return &admission{dir: dir, freed: make(chan struct{})} }

func TestPrioritiesKeepSlotsForThoseWhoWait(t *testing.T) {
	impatient(t)
	ctx := context.Background()
	slots := process(slotDir(t))
	for i := range Slots - 2 {
		if _, err := slots.acquire(ctx, Streaming); err != nil {
			t.Fatalf("stream %d: %v", i+1, err)
		}
	}
	if _, err := slots.acquire(ctx, Streaming); !errors.Is(err, ErrBusy) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a stream beyond its reach = %v, want ErrBusy", err)
	}
	waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := slots.acquire(waiting, Background); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("background work while streams fill its slots = %v, want it to wait", err)
	}
	var previews []func()
	for i := range 2 {
		release, err := slots.acquire(ctx, Interactive)
		if err != nil {
			t.Fatalf("preview %d with every stream playing: %v", i+1, err)
		}
		previews = append(previews, release)
	}
	if _, err := slots.acquire(ctx, Interactive); !errors.Is(err, ErrBusy) {
		t.Fatalf("a preview with every slot taken = %v, want ErrBusy", err)
	}
	// A slot this process frees goes to its waiter.
	got := make(chan error, 1)
	go func() { _, err := slots.acquire(ctx, Interactive); got <- err }()
	time.Sleep(20 * time.Millisecond)
	previews[0]()
	previews[0]() // releasing twice frees one slot
	if err := <-got; err != nil {
		t.Fatalf("waiting preview after a slot was freed: %v", err)
	}
	if _, err := slots.acquire(ctx, Interactive); !errors.Is(err, ErrBusy) {
		t.Fatalf("a double release freed a second slot: %v", err)
	}
}

func TestBackgroundWorkWaitsForAnotherProcess(t *testing.T) {
	impatient(t)
	ctx := context.Background()
	dir := slotDir(t)
	photos, product := process(dir), process(dir)
	var held []func()
	for range Slots {
		release, err := product.acquire(ctx, Interactive)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	if _, err := photos.acquire(ctx, Interactive); !errors.Is(err, ErrBusy) {
		t.Fatalf("another process's preview with every slot taken = %v, want ErrBusy", err)
	}
	got := make(chan error, 1)
	go func() { _, err := photos.acquire(ctx, Background); got <- err }()
	select {
	case err := <-got:
		t.Fatalf("background work with every slot taken returned %v, want it to wait", err)
	case <-time.After(400 * time.Millisecond):
	}
	// Slots above background work's reach do not help it.
	for _, release := range held[:Slots-2] {
		release()
	}
	select {
	case err := <-got:
		t.Fatalf("background work took a slot reserved for others: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	held[Slots-1]()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("background work after another process freed a slot: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background work did not notice the slot another process freed")
	}
}

func TestSlotsWithoutTheirDirectoryStayInTheProcess(t *testing.T) {
	impatient(t)
	slots := process(filepath.Join(t.TempDir(), "missing"))
	for range Slots {
		if _, err := slots.acquire(context.Background(), Interactive); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := slots.acquire(context.Background(), Interactive); !errors.Is(err, ErrBusy) {
		t.Fatalf("ninth request = %v, want ErrBusy", err)
	}
	unreadable := slotDir(t)
	if err := os.Remove(filepath.Join(unreadable, "7")); err != nil {
		t.Fatal(err)
	}
	if _, err := process(unreadable).acquire(context.Background(), Interactive); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a broken slot directory = %v, want ErrUnavailable", err)
	}
}

func TestRequestsGiveTheirSlotsBack(t *testing.T) {
	impatient(t)
	echo := script(t, "cat")
	local := Local{Tools: Tools{FFmpeg: echo, FFprobe: echo}}
	ctx := context.Background()
	probe := `{"streams":[],"format":{"format_name":"mp3","duration":"3"}}`
	for i := range 2 * Slots {
		if _, err := local.Probe(ctx, openFile(t, probe)); err != nil {
			t.Fatalf("probe %d: %v", i+1, err)
		}
		stream, err := local.Stream(ctx, openFile(t, "mp4"), StreamRequest{AudioTrack: -1})
		if err != nil {
			t.Fatalf("stream %d: %v", i+1, err)
		}
		_ = stream.Close()
		failing := Local{Tools: Tools{FFmpeg: script(t, "exit 1")}}
		if _, err := failing.Frame(ctx, openFile(t, "x"), 0, 640); !errors.Is(err, ErrUnreadable) {
			t.Fatalf("frame %d: %v", i+1, err)
		}
	}
}

// fakeWorker answers each connection on a socket with handle.
func fakeWorker(t *testing.T, handle func(*net.UnixConn)) Client {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "media.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				handle(conn)
			}()
		}
	}()
	return Client{Socket: socket, Slots: slotDir(t)}
}

func TestFailuresSayWhetherTheWorkerHadTheRequest(t *testing.T) {
	ctx := context.Background()
	dropped := fakeWorker(t, func(*net.UnixConn) {}) // systemd over MaxConnections
	if _, err := dropped.Image(ctx, openFile(t, "x"), 320); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInterrupted) {
		t.Fatalf("a dropped connection = %v, want ErrUnavailable only", err)
	}
	stopped := fakeWorker(t, func(conn *net.UnixConn) {
		var request Request
		file, err := readFrame(conn, &request)
		if err != nil || !request.Ack {
			t.Errorf("request = %+v, %v; want one asking for an acknowledgement", request, err)
			return
		}
		closeFile(file)
		_ = writeFrame(conn, Response{Accepted: true}, nil)
	})
	if _, err := stopped.Image(ctx, openFile(t, "x"), 320); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("a worker that stopped while working = %v, want ErrInterrupted", err)
	}
	answer := func(response Response, body string) func(*net.UnixConn) {
		return func(conn *net.UnixConn) {
			var request Request
			file, err := readFrame(conn, &request)
			if err != nil {
				return
			}
			closeFile(file)
			_ = writeFrame(conn, response, nil)
			_, _ = conn.Write([]byte(body))
		}
	}
	if _, err := fakeWorker(t, answer(Response{Code: CodeFailed, Message: "timed out"}, "")).Image(ctx, openFile(t, "x"), 320); !errors.Is(err, ErrFailed) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("a worker that ran out of time = %v, want ErrFailed", err)
	}
	// A worker from before acknowledgements answers straight away.
	data, err := fakeWorker(t, answer(Response{OK: true}, "jpeg")).Image(ctx, openFile(t, "x"), 320)
	if err != nil || string(data) != "jpeg" {
		t.Fatalf("an answer without an acknowledgement = %q, %v", data, err)
	}
}

func TestTimeoutsStartOnceARequestHasItsSlot(t *testing.T) {
	impatient(t)
	patience[Interactive] = 2 * time.Second
	slow := script(t, "sleep 0.3; cat")
	local := Local{Tools: Tools{FFmpeg: slow, FFprobe: slow}}
	slots, _ := admissions.LoadOrStore("", process(""))
	var held []func()
	for range Slots {
		release, err := slots.(*admission).acquire(context.Background(), Interactive)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	go func() {
		time.Sleep(time.Second)
		held[0]()
	}()
	defer func() {
		for _, release := range held {
			release()
		}
	}()
	// Waiting a second for the slot does not use up its half second.
	frame, err := local.Frame(WithTimeout(context.Background(), 500*time.Millisecond), openFile(t, "jpeg"), 0, 640)
	if err != nil || string(frame) != "jpeg" {
		t.Fatalf("Frame() after waiting for a slot = %q, %v", frame, err)
	}
	if _, err := local.Frame(WithTimeout(context.Background(), 100*time.Millisecond), openFile(t, "jpeg"), 0, 640); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Frame() slower than its bound = %v, want DeadlineExceeded", err)
	}
}

func TestStreamsAndBackgroundWorkHaveTheirOwnPriority(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		ctx  context.Context
		op   string
		want Priority
	}{
		{ctx, OpImage, Interactive},
		{ctx, OpProbe, Interactive},
		{ctx, OpStream, Streaming},
		{ctx, OpAudio, Streaming},
		{WithPriority(ctx, Background), OpProbe, Background},
		{WithPriority(ctx, Interactive), OpStream, Interactive},
		{WithPriority(ctx, Priority(42)), OpStream, Streaming},
	} {
		if got := priorityOf(tc.ctx, tc.op); got != tc.want {
			t.Errorf("priority of %s = %d, want %d", tc.op, got, tc.want)
		}
	}
}
