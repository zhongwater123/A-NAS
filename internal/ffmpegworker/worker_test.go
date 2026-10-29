package ffmpegworker

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
)

func TestRequestsAreBoundedBeforeAnyCommandRuns(t *testing.T) {
	valid := []Request{
		{Op: OpProbe},
		{Op: OpFrame, At: 12.5, Width: 640},
		{Op: OpSubtitle, Format: "srt", Charset: "GB18030", Stream: -1},
		{Op: OpSubtitle, Stream: 3},
		{Op: OpStream, Start: 60, CopyVideo: true, HEVC: true, AudioTrack: 1},
		{Op: OpStream, Height: 720, MaxBitrate: 4000, AudioTrack: -1},
		{Op: OpAudio, Bits: 16},
		{Op: OpAudio, Start: 42.5, SampleRate: 176400, Bits: 24},
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
		{Op: OpAudio},
		{Op: OpAudio, Bits: 32},
		{Op: OpAudio, Bits: 16, SampleRate: 1},
		{Op: OpAudio, Bits: 16, Start: -1},
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
		{Op: OpAudio, Bits: 16},
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
	_, decoded := tools.command(Request{Op: OpAudio, Start: 30, SampleRate: 176400, Bits: 24})
	if joined := strings.Join(decoded, " "); !strings.Contains(joined, "-ss 30.000 -i fd: -map 0:a:0 -vn") || !strings.Contains(joined, "-ar 176400") ||
		!strings.Contains(joined, "-c:a flac") || !strings.Contains(joined, "-sample_fmt s32 -bits_per_raw_sample 24") || !strings.HasSuffix(joined, "-f flac pipe:1") {
		t.Errorf("audio command = %s", joined)
	}
	_, plain := tools.command(Request{Op: OpAudio, Bits: 16})
	if joined := strings.Join(plain, " "); strings.Contains(joined, "-ss") || strings.Contains(joined, "-ar") || !strings.Contains(joined, "-sample_fmt s16") {
		t.Errorf("audio command from the start = %s", joined)
	}
}

func TestProbeOutputIsParsed(t *testing.T) {
	probe, err := ParseProbe([]byte(`{
  "streams": [
    {"index": 0, "codec_type": "video", "codec_name": "hevc", "width": 3840, "height": 2160, "avg_frame_rate": "24000/1001",
     "color_transfer": "smpte2084", "side_data_list": [{"side_data_type": "DOVI configuration record"}]},
    {"index": 1, "codec_type": "audio", "codec_name": "alac", "channels": 2, "sample_rate": "96000", "sample_fmt": "s32p",
     "bits_per_raw_sample": "24", "disposition": {"default": 1}, "tags": {"language": "chi"}},
    {"index": 2, "codec_type": "video", "codec_name": "mjpeg", "disposition": {"attached_pic": 1}},
    {"index": 3, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080,
     "side_data_list": [{"side_data_type": "Display Matrix", "displaymatrix": "...", "rotation": -90}]},
    {"index": 4, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080, "tags": {"rotate": "90"}}
  ],
  "format": {"format_name": "mov,mp4,m4a", "duration": "251.480000", "bit_rate": "2100000", "tags": {"title": "晴天", "artist": "周杰伦"}}
}`))
	if err != nil {
		t.Fatalf("ParseProbe() error = %v", err)
	}
	if probe.Format.Name != "mov,mp4,m4a" || probe.Format.Duration != 251.48 || probe.Format.Tags["title"] != "晴天" || len(probe.Streams) != 5 {
		t.Fatalf("probe = %+v", probe)
	}
	video, audio, cover := probe.Streams[0], probe.Streams[1], probe.Streams[2]
	if video.FrameRate < 23.97 || video.FrameRate > 23.98 || video.ColorTransfer != "smpte2084" || len(video.SideData) != 1 {
		t.Errorf("video = %+v", video)
	}
	if audio.Codec != "alac" || audio.SampleRate != 96000 || audio.Bits != 24 || audio.SampleFormat != "s32p" || !audio.Default || audio.Tags["language"] != "chi" {
		t.Errorf("audio = %+v", audio)
	}
	if !cover.AttachedPic {
		t.Errorf("cover = %+v", cover)
	}
	if matrix, tag := probe.Streams[3], probe.Streams[4]; matrix.Rotation != -90 || tag.Rotation != -90 || video.Rotation != 0 {
		t.Errorf("rotations = %d, %d, %d", matrix.Rotation, tag.Rotation, video.Rotation)
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
	if err != nil || info.Streams[0].Height != 1080 || info.Format.Duration != 60 {
		t.Fatalf("Probe() = %+v, %v", info, err)
	}
	frame, err := local.Frame(ctx, openFile(t, "jpeg bytes"), 6, 640)
	if err != nil || string(frame) != "jpeg bytes" {
		t.Fatalf("Frame() = %q, %v", frame, err)
	}
	stream, err := local.Stream(ctx, openFile(t, "fragmented mp4"), StreamRequest{AudioTrack: -1})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	data, _ := io.ReadAll(stream)
	_ = stream.Close()
	if string(data) != "fragmented mp4" {
		t.Fatalf("stream = %q", data)
	}
	decoded, err := local.Audio(ctx, openFile(t, "flac bytes"), AudioRequest{Bits: 16})
	if err != nil {
		t.Fatalf("Audio() error = %v", err)
	}
	data, _ = io.ReadAll(decoded)
	_ = decoded.Close()
	if string(data) != "flac bytes" {
		t.Fatalf("audio = %q", data)
	}
	if _, err := local.Audio(ctx, openFile(t, "x"), AudioRequest{Bits: 8}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Audio() with 8 bits = %v", err)
	}

	failing := Local{Tools: Tools{FFmpeg: script(t, "echo 'Invalid data found when processing input' >&2; exit 1"), FFprobe: script(t, "exit 1")}}
	if _, err := failing.Frame(ctx, openFile(t, "x"), 0, 640); !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "Invalid data") {
		t.Fatalf("Frame() of a bad file = %v", err)
	}
	if _, err := failing.Stream(ctx, openFile(t, "x"), StreamRequest{AudioTrack: -1}); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Stream() of a bad file = %v", err)
	}
	if _, err := (Client{Socket: filepath.Join(t.TempDir(), "missing.sock")}).Probe(ctx, openFile(t, "x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Probe() without a worker = %v", err)
	}
}

func TestClosingAStreamStopsFFmpeg(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "finished")
	// Writes forever until its output is closed.
	endless := script(t, "trap 'exit 0' PIPE; while true; do echo data || break; done; touch "+marker)
	local := Local{Tools: Tools{FFmpeg: endless, FFprobe: endless}}
	stream, err := local.Stream(context.Background(), openFile(t, "x"), StreamRequest{AudioTrack: -1})
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
	defer func() { _ = file.Close() }()
	local := Local{Tools: tools}
	ctx := context.Background()
	info, err := local.Probe(ctx, file)
	if err != nil || len(info.Streams) != 2 || info.Streams[0].Codec != "h264" || info.Streams[1].Codec != "ac3" || info.Format.Duration < 3.9 {
		t.Fatalf("Probe() = %+v, %v", info, err)
	}
	jpeg, err := local.Frame(ctx, file, 2, 160)
	if err != nil || !bytes.HasPrefix(jpeg, []byte{0xff, 0xd8}) {
		t.Fatalf("Frame() = %d bytes, %v", len(jpeg), err)
	}
	stream, err := local.Stream(ctx, file, StreamRequest{Start: 1, CopyVideo: true, AudioTrack: 0})
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
	defer func() { _ = subtitleFile.Close() }()
	vtt, err := local.Subtitle(ctx, subtitleFile, SubtitleRequest{Format: "srt", Stream: -1})
	if err != nil || !bytes.HasPrefix(vtt, []byte("WEBVTT")) || !bytes.Contains(vtt, []byte("你好")) {
		t.Fatalf("Subtitle() = %q, %v", vtt, err)
	}
}

// TestRealFFmpegAudio decodes lossless and lossy audio to FLAC with FFmpeg
// when it is installed.
func TestRealFFmpegAudio(t *testing.T) {
	tools, ok := LocalTools()
	if !ok {
		t.Skip("FFmpeg is not installed")
	}
	local := Local{Tools: tools}
	ctx := context.Background()
	for _, sample := range []struct{ name, codec string }{{"hi.aiff", "pcm_s24be"}, {"lossy.wma", "wmav2"}, {"hi.wv", "wavpack"}} {
		path := filepath.Join(t.TempDir(), sample.name)
		generate := exec.Command(tools.FFmpeg, "-hide_banner", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=3:sample_rate=96000",
			"-ac", "2", "-c:a", sample.codec, "-metadata", "title=测试", path)
		if output, err := generate.CombinedOutput(); err != nil {
			t.Skipf("FFmpeg cannot generate %s: %v %s", sample.name, err, output)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		probe, err := local.Probe(ctx, file)
		if err != nil || probe.Format.Duration < 2.9 || len(probe.Streams) != 1 || probe.Streams[0].SampleRate == 0 {
			t.Fatalf("%s: Probe() = %+v, %v", sample.name, probe, err)
		}
		decoded, err := local.Audio(ctx, file, AudioRequest{Start: 1, SampleRate: 48000, Bits: 24})
		if err != nil {
			t.Fatalf("%s: Audio() error = %v", sample.name, err)
		}
		data, _ := io.ReadAll(decoded)
		_ = decoded.Close()
		_ = file.Close()
		// Two seconds of 24-bit stereo at 48 kHz, losslessly compressed.
		if !bytes.HasPrefix(data, []byte("fLaC")) || len(data) < 20_000 {
			t.Fatalf("%s: audio = %d bytes starting %q", sample.name, len(data), data[:min(4, len(data))])
		}
		check := filepath.Join(t.TempDir(), "check.flac")
		if err := os.WriteFile(check, data, 0o600); err != nil {
			t.Fatal(err)
		}
		output, err := exec.Command(tools.FFprobe, "-v", "error", "-show_entries", "stream=sample_rate,bits_per_raw_sample", "-of", "csv=p=0", check).Output()
		if err != nil || strings.TrimSpace(string(output)) != "48000,24" {
			t.Fatalf("%s: decoded stream = %q, %v", sample.name, output, err)
		}
	}
}
