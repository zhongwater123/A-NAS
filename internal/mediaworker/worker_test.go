package mediaworker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/media"
)

func TestRequestsAreBoundedBeforeAnyCommandRuns(t *testing.T) {
	valid := []Request{
		{Op: OpProbe},
		{Op: OpFrame, At: 12.5, Width: 640},
		{Op: OpSubtitle, Format: "srt", Charset: "GB18030", Stream: -1},
		{Op: OpSubtitle, Stream: 3},
		{Op: OpStream, Start: 60, CopyVideo: true, HEVC: true, AudioTrack: 1},
		{Op: OpStream, Height: 720, MaxBitrate: 4000, AudioTrack: -1},
	}
	for _, request := range valid {
		if err := request.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", request, err)
		}
	}
	invalid := []Request{
		{Op: "shell"},
		{Op: OpFrame, At: -1, Width: 640},
		{Op: OpFrame, Width: 100000},
		{Op: OpSubtitle, Format: "../../etc/passwd", Stream: -1},
		{Op: OpSubtitle, Format: "srt", Charset: "-i /etc/shadow", Stream: -1},
		{Op: OpStream, Height: 99999, AudioTrack: -1},
		{Op: OpStream, Start: -5, AudioTrack: -1},
		{Op: OpStream, HEVC: true, AudioTrack: -1},
	}
	for _, request := range invalid {
		if err := request.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted the request", request)
		}
	}
}

func TestCommandsReadTheDescriptorAndWriteStandardOutput(t *testing.T) {
	tools := Tools{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"}
	for _, request := range []Request{
		{Op: OpProbe}, {Op: OpFrame, At: 30, Width: 640}, {Op: OpSubtitle, Format: "ass", Stream: -1},
		{Op: OpStream, Start: 90, CopyVideo: true, AudioTrack: 0}, {Op: OpStream, Height: 480, MaxBitrate: 1500, AudioTrack: 2},
	} {
		program, args := tools.command(request)
		joined := strings.Join(args, " ")
		if !slices.Contains(args, "fd:") || program == "" {
			t.Errorf("%s does not read fd: %s %s", request.Op, program, joined)
		}
		if request.Op != OpProbe && args[len(args)-1] != "pipe:1" {
			t.Errorf("%s does not write standard output: %s", request.Op, joined)
		}
		if request.Op != OpProbe && !slices.Contains(args, "-nostdin") {
			t.Errorf("%s may read keyboard commands from the input: %s", request.Op, joined)
		}
	}
	_, copied := tools.command(Request{Op: OpStream, Start: 90, CopyVideo: true, HEVC: true, AudioTrack: 0, CopyAudio: true})
	if joined := strings.Join(copied, " "); !strings.Contains(joined, "-ss 90.000 -i fd:") || !strings.Contains(joined, "-c:v copy -tag:v hvc1") ||
		!strings.Contains(joined, "-c:a copy") || !strings.Contains(joined, "frag_keyframe+empty_moov") {
		t.Errorf("remux command = %s", joined)
	}
	_, transcoded := tools.command(Request{Op: OpStream, Height: 720, MaxBitrate: 4000, AudioTrack: -1})
	if joined := strings.Join(transcoded, " "); !strings.Contains(joined, "libx264") || !strings.Contains(joined, "min(720,ih)") ||
		!strings.Contains(joined, "-maxrate 4000k") || !strings.Contains(joined, "-an") {
		t.Errorf("transcode command = %s", joined)
	}
}

func TestProbeOutputBecomesMediaInfo(t *testing.T) {
	info, err := ParseProbe([]byte(`{
  "streams": [
    {"index": 0, "codec_type": "video", "codec_name": "hevc", "width": 3840, "height": 2160, "pix_fmt": "yuv420p10le",
     "avg_frame_rate": "24000/1001", "color_transfer": "smpte2084", "side_data_list": [{"side_data_type": "DOVI configuration record"}]},
    {"index": 1, "codec_type": "audio", "codec_name": "eac3", "channels": 6, "disposition": {"default": 1}, "tags": {"language": "chi", "title": "国语"}},
    {"index": 2, "codec_type": "audio", "codec_name": "aac", "channels": 2, "tags": {"language": "und"}},
    {"index": 3, "codec_type": "subtitle", "codec_name": "subrip", "tags": {"language": "chi"}},
    {"index": 4, "codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle"},
    {"index": 5, "codec_type": "video", "codec_name": "mjpeg", "disposition": {"attached_pic": 1}}
  ],
  "format": {"format_name": "matroska,webm", "duration": "7212.480000", "bit_rate": "21000000"}
}`))
	if err != nil {
		t.Fatalf("ParseProbe() error = %v", err)
	}
	if info.Container != "matroska,webm" || info.Duration != 7212.48 || info.Video == nil || info.Video.Codec != "hevc" ||
		info.Video.HDR != "Dolby Vision" || info.Video.FrameRate < 23.97 || info.Video.FrameRate > 23.98 {
		t.Fatalf("info = %+v / %+v", info, info.Video)
	}
	if len(info.Audio) != 2 || info.Audio[0].Title != "国语" || !info.Audio[0].Default || info.Audio[1].Language != "" || len(info.Subtitles) != 2 {
		t.Fatalf("streams = %+v %+v", info.Audio, info.Subtitles)
	}
	if _, err := ParseProbe([]byte(`{"streams": []}`)); err == nil {
		t.Fatal("a probe without a container parsed")
	}
}

// script writes an executable stand-in for FFmpeg.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func openFile(t *testing.T, contents string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestTheWorkerReceivesTheDescriptorAndAnswers(t *testing.T) {
	// The stand-in tools echo their standard input, which is the passed file.
	echo := script(t, "cat")
	local := Local{Tools: Tools{FFmpeg: echo, FFprobe: echo}}
	ctx := context.Background()
	probe := `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"format_name":"mov,mp4","duration":"60"}}`
	info, err := local.Probe(ctx, openFile(t, probe))
	if err != nil || info.Video.Height != 1080 || info.Duration != 60 {
		t.Fatalf("Probe() = %+v, %v", info, err)
	}
	frame, err := local.Frame(ctx, openFile(t, "jpeg bytes"), 6, 640)
	if err != nil || string(frame) != "jpeg bytes" {
		t.Fatalf("Frame() = %q, %v", frame, err)
	}
	stream, err := local.Stream(ctx, openFile(t, "fragmented mp4"), media.StreamOptions{AudioTrack: -1})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	data, _ := io.ReadAll(stream)
	_ = stream.Close()
	if string(data) != "fragmented mp4" {
		t.Fatalf("stream = %q", data)
	}

	failing := Local{Tools: Tools{FFmpeg: script(t, "echo 'Invalid data found when processing input' >&2; exit 1"), FFprobe: script(t, "exit 1")}}
	if _, err := failing.Frame(ctx, openFile(t, "x"), 0, 640); !errors.Is(err, media.ErrUnreadable) || !strings.Contains(err.Error(), "Invalid data") {
		t.Fatalf("Frame() of a bad file = %v", err)
	}
	if _, err := failing.Stream(ctx, openFile(t, "x"), media.StreamOptions{AudioTrack: -1}); !errors.Is(err, media.ErrUnreadable) {
		t.Fatalf("Stream() of a bad file = %v", err)
	}
	if _, err := (Client{Socket: filepath.Join(t.TempDir(), "missing.sock")}).Probe(ctx, openFile(t, "x")); !errors.Is(err, media.ErrUnavailable) {
		t.Fatalf("Probe() without a worker = %v", err)
	}
}

func TestClosingAStreamStopsFFmpeg(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "finished")
	// Writes forever until its output is closed.
	endless := script(t, "trap 'exit 0' PIPE; while true; do echo data || break; done; touch "+marker)
	local := Local{Tools: Tools{FFmpeg: endless, FFprobe: endless}}
	stream, err := local.Stream(context.Background(), openFile(t, "x"), media.StreamOptions{AudioTrack: -1})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	buffer := make([]byte, 16)
	if _, err := io.ReadFull(stream, buffer); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	_ = stream.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out, _ := exec.Command("pgrep", "-f", endless).Output(); len(bytes.TrimSpace(out)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("FFmpeg kept running after the stream was closed")
}

// TestRealFFmpeg runs the commands against FFmpeg when it is installed.
func TestRealFFmpeg(t *testing.T) {
	tools, ok := LocalTools()
	if !ok {
		t.Skip("FFmpeg is not installed")
	}
	sample := filepath.Join(t.TempDir(), "sample.mkv")
	generate := exec.Command(tools.FFmpeg, "-hide_banner", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "ac3", sample)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("FFmpeg cannot generate a sample: %v %s", err, output)
	}
	file, err := os.Open(sample)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	local := Local{Tools: tools}
	ctx := context.Background()
	info, err := local.Probe(ctx, file)
	if err != nil || info.Video == nil || info.Video.Codec != "h264" || len(info.Audio) != 1 || info.Audio[0].Codec != "ac3" || info.Duration < 3.9 {
		t.Fatalf("Probe() = %+v, %v", info, err)
	}
	jpeg, err := local.Frame(ctx, file, 2, 160)
	if err != nil || !bytes.HasPrefix(jpeg, []byte{0xff, 0xd8}) {
		t.Fatalf("Frame() = %d bytes, %v", len(jpeg), err)
	}
	stream, err := local.Stream(ctx, file, media.StreamOptions{Start: 1, CopyVideo: true, AudioTrack: 0})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	data, _ := io.ReadAll(stream)
	_ = stream.Close()
	if len(data) < 1000 || !bytes.Contains(data[:64], []byte("ftyp")) {
		t.Fatalf("stream = %d bytes", len(data))
	}
	subtitles := filepath.Join(t.TempDir(), "sample.srt")
	if err := os.WriteFile(subtitles, []byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	subtitleFile, err := os.Open(subtitles)
	if err != nil {
		t.Fatal(err)
	}
	defer subtitleFile.Close()
	vtt, err := local.Subtitle(ctx, subtitleFile, media.SubtitleSource{Format: "srt", Stream: -1})
	if err != nil || !bytes.HasPrefix(vtt, []byte("WEBVTT")) || !bytes.Contains(vtt, []byte("你好")) {
		t.Fatalf("Subtitle() = %q, %v", vtt, err)
	}
}
