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
// what answer makes of the request and its descriptors.
func worker(t *testing.T, answer func(request map[string]any, files []*os.File) any) string {
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
		_ = unixmsg.Write(conn.(*net.UnixConn), answer(request, files), nil, 1<<20)
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

func TestTheMusicWorkerReadsBothStreamsInOrder(t *testing.T) {
	var got []string
	socket := worker(t, func(request map[string]any, files []*os.File) any {
		for _, file := range files {
			data, _ := io.ReadAll(file)
			got = append(got, string(data))
		}
		streams, _ := json.Marshal(request["streams"])
		got = append(got, string(streams))
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
	analysis, err := montage.MusicWorker{Socket: socket}.Analyze(context.Background(), stereo, mono)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "stereo" || got[1] != "mono" ||
		got[2] != `[{"channels":2,"name":"stereo","rate":44100},{"channels":1,"name":"mono","rate":22050}]` {
		t.Fatalf("the worker got %q", got)
	}
	if analysis.Structure.Model != "allin1" || len(analysis.Loudness) != 2 || analysis.Loudness[1] != 0.5 || analysis.Seconds != 3.5 {
		t.Fatalf("analysis = %+v", analysis)
	}
}

func TestAWorkerThatRefusesOrIsAwaySaysSo(t *testing.T) {
	refusing := worker(t, func(map[string]any, []*os.File) any {
		return map[string]any{"ok": false, "error": map[string]any{"code": "invalid_input", "message": "no song"}}
	})
	stereo, _, _ := os.Pipe()
	mono, _, _ := os.Pipe()
	if _, err := (montage.MusicWorker{Socket: refusing}).Analyze(context.Background(), stereo, mono); !errors.Is(err, montage.ErrWorkerRefused) {
		t.Fatalf("Analyze() = %v; want refused", err)
	}
	away := filepath.Join(t.TempDir(), "none.sock")
	if _, err := (montage.MusicWorker{Socket: away}).Analyze(context.Background(), stereo, mono); !errors.Is(err, montage.ErrWorkerUnavailable) {
		t.Fatalf("Analyze() = %v; want unavailable", err)
	}
}
