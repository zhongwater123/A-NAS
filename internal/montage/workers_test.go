package montage_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// worker answers one connection on a socket in dir as a Worker would, with
// what answer makes of the request and its descriptors. conn is there to
// read what the caller sends after the request.
func worker(t *testing.T, answer func(request map[string]any, files []*os.File, conn *net.UnixConn) any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var request map[string]any
		files, err := unixmsg.ReadFiles(conn.(*net.UnixConn), &request, 1<<20, 4)
		if err != nil {
			t.Error(err)
			return
		}
		defer unixmsg.CloseFiles(files)
		if reply := answer(request, files, conn.(*net.UnixConn)); reply != nil {
			_ = unixmsg.Write(conn.(*net.UnixConn), reply, nil, 1<<20)
		}
	}()
	return path
}

func float32s(values ...float32) []byte {
	out := make([]byte, 0, 4*len(values))
	for _, v := range values {
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v))
	}
	return out
}

// written is what a decoding that finished tells the analyzer.
func written(err error) <-chan error {
	whole := make(chan error, 1)
	whole <- err
	return whole
}

func TestTheMusicWorkerReadsBothStreamsInOrder(t *testing.T) {
	var got []string
	socket := worker(t, func(request map[string]any, files []*os.File, conn *net.UnixConn) any {
		for _, file := range files {
			data, _ := io.ReadAll(file)
			got = append(got, string(data))
		}
		streams, _ := json.Marshal(request["streams"])
		got = append(got, string(streams))
		// The streams are analysed once the caller confirms them whole.
		var confirmation json.RawMessage
		if _, err := unixmsg.ReadFiles(conn, &confirmation, 1<<20, 0); err != nil || request["confirm"] != true {
			t.Errorf("confirm = %v, then %v", request["confirm"], err)
		}
		got = append(got, string(confirmation))
		return map[string]any{"ok": true, "model": "allin1", "bpm": 120, "beats": []float64{0.5}, "downbeats": []float64{0.5},
			"segments": []map[string]any{{"start": 0, "end": 1, "label": "intro"}}, "loudness": base64.StdEncoding.EncodeToString(float32s(0.25, 0.5)), "seconds": 3.5}
	})
	stereo, stereoIn, _ := os.Pipe()
	mono, monoIn, _ := os.Pipe()
	go func() {
		_, _ = stereoIn.WriteString("stereo")
		_ = stereoIn.Close()
		_, _ = monoIn.WriteString("mono")
		_ = monoIn.Close()
	}()
	analysis, err := montage.MusicWorker{Socket: socket}.Analyze(context.Background(), stereo, mono, written(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0] != "stereo" || got[1] != "mono" ||
		got[2] != `[{"channels":2,"name":"stereo","rate":44100},{"channels":1,"name":"mono","rate":22050}]` ||
		got[3] != `{"streams":"complete"}` {
		t.Fatalf("the worker got %q", got)
	}
	if analysis.Structure.Model != "allin1" || len(analysis.Loudness) != 2 || analysis.Loudness[1] != 0.5 || analysis.Seconds != 3.5 {
		t.Fatalf("analysis = %+v", analysis)
	}
}

// A Worker that refuses the streams answers while they are still being
// written; one that is away was never asked; one that took the song and broke
// spent an attempt.
func TestAWorkerThatRefusesOrIsAwayOrFailsSaysSo(t *testing.T) {
	failure := func(code string) func(map[string]any, []*os.File, *net.UnixConn) any {
		return func(map[string]any, []*os.File, *net.UnixConn) any {
			return map[string]any{"ok": false, "error": map[string]any{"code": code, "message": "no song"}}
		}
	}
	stereo, _, _ := os.Pipe()
	mono, _, _ := os.Pipe()
	writing := make(chan error)
	for code, want := range map[string]error{"invalid_input": montage.ErrWorkerRefused, "unavailable": montage.ErrWorkerUnavailable, "internal": montage.ErrWorkerFailed} {
		if _, err := (montage.MusicWorker{Socket: worker(t, failure(code))}).Analyze(context.Background(), stereo, mono, writing); !errors.Is(err, want) {
			t.Fatalf("Analyze() answered %s = %v; want %v", code, err, want)
		}
	}
	away := filepath.Join(t.TempDir(), "none.sock")
	if _, err := (montage.MusicWorker{Socket: away}).Analyze(context.Background(), stereo, mono, writing); !errors.Is(err, montage.ErrWorkerUnavailable) {
		t.Fatalf("Analyze() = %v; want unavailable", err)
	}
	// A Worker that stops in the middle of a song, as one out of memory.
	gone := worker(t, func(map[string]any, []*os.File, *net.UnixConn) any { return nil })
	if _, err := (montage.MusicWorker{Socket: gone}).Analyze(context.Background(), stereo, mono, written(nil)); !errors.Is(err, montage.ErrWorkerFailed) {
		t.Fatalf("Analyze() with a worker that stopped = %v; want failed", err)
	}
}

// The end of a pipe does not tell a whole song from one whose decoding broke
// off. When it did, the caller hangs up instead of confirming, and the error
// is the decoding's.
func TestStreamsCutShortAreNotConfirmed(t *testing.T) {
	seen := make(chan error, 1)
	socket := worker(t, func(_ map[string]any, files []*os.File, conn *net.UnixConn) any {
		for _, file := range files {
			_, _ = io.Copy(io.Discard, file)
		}
		var confirmation json.RawMessage
		_, err := unixmsg.ReadFiles(conn, &confirmation, 1<<20, 0)
		seen <- err
		return nil
	})
	stereo, stereoIn, _ := os.Pipe()
	mono, monoIn, _ := os.Pipe()
	_ = stereoIn.Close()
	_ = monoIn.Close()
	broke := errors.New("the media worker stopped")
	if _, err := (montage.MusicWorker{Socket: socket}).Analyze(context.Background(), stereo, mono, written(broke)); !errors.Is(err, broke) {
		t.Fatalf("Analyze() = %v; want the decoding's error", err)
	}
	if err := <-seen; err == nil {
		t.Fatal("the worker was told the streams were whole")
	}
}
