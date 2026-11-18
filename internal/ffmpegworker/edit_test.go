package ffmpegworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

func TestEditingRequestsAreBoundedBeforeFFmpegRuns(t *testing.T) {
	jpeg := func(r FramesRequest) FramesRequest { r.Format, r.Quality = FormatJPEG, 3; return r }
	gray := func(r FramesRequest) FramesRequest { r.Format = FormatGray; return r }
	valid := []FramesRequest{
		jpeg(FramesRequest{Times: []float64{0, 1.5, 3}, Width: 512, Blur: true}),
		jpeg(FramesRequest{Times: []float64{1}, LongEdge: 512}),
		jpeg(FramesRequest{Start: 2, End: 62, Rate: 0.5, LongEdge: 1920}),
		jpeg(FramesRequest{End: 300, Rate: 2, Width: 64}),
		gray(FramesRequest{Start: 0, End: 30, Rate: 10, Width: 320, Height: 180}),
		gray(FramesRequest{Times: []float64{4.25}, Width: 1920, Height: 3414, Turned: true}),
		// 64 MiB of gray frames exactly: 1,024 frames of 256×256.
		gray(FramesRequest{End: 1024, Rate: 1, Width: 256, Height: 256}),
	}
	for _, request := range valid {
		if _, err := request.count(); err != nil {
			t.Errorf("count(%+v) = %v", request, err)
		}
	}
	many := make([]float64, maxFrames+1)
	for i := range many {
		many[i] = float64(i)
	}
	invalid := []FramesRequest{
		{Times: []float64{1}, Width: 512, Quality: 3},
		jpeg(FramesRequest{Width: 512}),
		jpeg(FramesRequest{Times: []float64{2, 1}, Width: 512}),
		jpeg(FramesRequest{Times: []float64{1, 1}, Width: 512}),
		jpeg(FramesRequest{Times: []float64{-1}, Width: 512}),
		jpeg(FramesRequest{Times: []float64{1}, Start: 1, End: 2, Rate: 1, Width: 512}),
		jpeg(FramesRequest{Times: many[:maxJPEGFrames+1], Width: 512}),
		jpeg(FramesRequest{Times: []float64{1}, Width: 512, LongEdge: 512}),
		jpeg(FramesRequest{Times: []float64{1}, Width: 4000}),
		jpeg(FramesRequest{Times: []float64{1}, Width: 32}),
		jpeg(FramesRequest{Times: []float64{1}, Width: 512, Height: 288}),
		{Times: []float64{1}, Width: 512, Format: FormatJPEG, Quality: 100},
		jpeg(FramesRequest{Start: 5, End: 5, Rate: 1, Width: 512}),
		jpeg(FramesRequest{Start: 0, End: 10, Rate: 120, Width: 512}),
		jpeg(FramesRequest{End: 300.5, Rate: 2, Width: 64}),
		gray(FramesRequest{Times: many, Width: 16, Height: 16}),
		gray(FramesRequest{End: 1025, Rate: 1, Width: 256, Height: 256}),
		gray(FramesRequest{Times: []float64{1}, Width: 320}),
		gray(FramesRequest{Times: []float64{1}, Width: 320, Height: 180, Blur: true}),
		gray(FramesRequest{Times: []float64{1}, Width: 5000, Height: 180}),
		{Times: []float64{1}, Width: 320, Height: 180, Format: "png"},
	}
	for _, request := range invalid {
		if _, err := request.count(); err == nil {
			t.Errorf("count(%+v) accepted the request", request)
		}
	}

	for _, request := range []SegmentRequest{
		{Frames: 360, Offset: 0}, {Start: 12.5, Frames: 45, Slow: 2.5, Turned: true, Blurred: true, FadeFrames: 30, Offset: 4455},
	} {
		if err := request.validate(); err != nil {
			t.Errorf("validate(%+v) = %v", request, err)
		}
	}
	for _, request := range []SegmentRequest{
		{Frames: 0}, {Frames: 361}, {Start: -1, Frames: 30}, {Frames: 30, Slow: 0.5}, {Frames: 30, Slow: 3},
		{Frames: 30, FadeFrames: 31}, {Frames: 30, Offset: -1}, {Frames: 30, Offset: 4471}, {Frames: 30, Offset: math.MaxInt},
	} {
		if err := request.validate(); err == nil {
			t.Errorf("validate(%+v) accepted the request", request)
		}
	}
	if _, err := (AssembleRequest{SongStart: 63.7, Frames: 2700, Created: "2026-08-04T18:30:00+08:00"}).validate(); err != nil {
		t.Errorf("valid assemble request: %v", err)
	}
	for _, request := range []AssembleRequest{
		{Frames: 2700}, {Frames: 2700, Created: "yesterday"}, {Frames: 4501, Created: "2026-08-04T18:30:00Z"},
		{SongStart: -1, Frames: 30, Created: "2026-08-04T18:30:00Z"},
	} {
		if _, err := request.validate(); err == nil {
			t.Errorf("validate(%+v) accepted the request", request)
		}
	}
}

func TestEditingCommandsAreBuiltFromNumbers(t *testing.T) {
	tools := Tools{FFmpeg: "/usr/bin/ffmpeg"}
	_, args := tools.frameCommand(FramesRequest{Format: FormatJPEG, Width: 512, Quality: 3, Turned: true}, 2.5)
	if joined := strings.Join(args, " "); !strings.Contains(joined, "-ss 2.500 -i fd:") || !strings.Contains(joined, "-vf hflip,vflip,scale=512:-2") ||
		!strings.HasSuffix(joined, "-q:v 3 -c:v mjpeg -f image2 pipe:1") || !strings.Contains(joined, "-filter_threads 4 -threads 4") {
		t.Errorf("frame command = %s", joined)
	}
	_, args = tools.rangeCommand(FramesRequest{Format: FormatGray, Start: 1, End: 3, Rate: 10, Width: 320, Height: 180})
	if joined := strings.Join(args, " "); !strings.Contains(joined, "-ss 1.000 -t 2.000 -i fd:") || !strings.Contains(joined, "fps=10,scale=320:180,format=gray") ||
		!strings.HasSuffix(joined, "-f rawvideo pipe:1") {
		t.Errorf("range command = %s", joined)
	}
	_, args = tools.segmentCommand(SegmentRequest{Start: 3, Frames: 75, Slow: 2.5, Blurred: true, FadeFrames: 30, Offset: 150})
	joined := strings.Join(args, " ")
	for _, want := range []string{"-filter_threads 4", "-ss 3.000 -t 1.066667 -i fd:", "gblur=sigma=12", ",setpts=2.5*PTS,fps=30,tpad=stop_mode=clone:stop=-1,setpts=PTS-STARTPTS,trim=end_frame=75",
		"fade=t=out:start_frame=45:nb_frames=30", "-frames:v 75", "-crf 23", "-maxrate 16M -bufsize 16M", "-avoid_negative_ts disabled",
		"-output_ts_offset 5.000000 -f mpegts pipe:1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("segment command lacks %q: %s", want, joined)
		}
	}
	_, args = tools.assembleCommand(AssembleRequest{SongStart: 63.7, Frames: 2700}, time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC))
	joined = strings.Join(args, " ")
	for _, want := range []string{"-y -i fd: -ss 63.700 -t 90.000 -fd 3 -i fd:", "-c:v copy -c:a aac -b:a 192k", "afade=t=in:d=0.3,afade=t=out:st=87.500:d=2.5",
		"creation_time=2026-08-04T10:30:00.000000Z", "-movflags +faststart", "-fs 536870912 -f mp4 /proc/self/fd/4"} {
		if !strings.Contains(joined, want) {
			t.Errorf("assemble command lacks %q: %s", want, joined)
		}
	}
	_, args = tools.command(Request{Op: OpAudio, Start: 10, SampleRate: 22050, Bits: 32, PCM: true, Channels: 1})
	if joined := strings.Join(args, " "); !strings.HasSuffix(joined, "-i fd: -map 0:a:0 -vn -sn -dn -map_metadata -1 -ac 1 -ar 22050 -c:a pcm_f32le -f f32le pipe:1") {
		t.Errorf("PCM command = %s", joined)
	}
	_, args = tools.command(Request{Op: OpAudio, SampleRate: 44100, Bits: 32, PCM: true, Channels: 2})
	if joined := strings.Join(args, " "); !strings.HasSuffix(joined, "-ac 2 -ar 44100 -c:a pcm_f32le -f f32le pipe:1") {
		t.Errorf("stereo PCM command = %s", joined)
	}
	for _, request := range []Request{
		{Op: OpAudio, SampleRate: 22050, Bits: 16, PCM: true, Channels: 1},
		{Op: OpAudio, SampleRate: 96000, Bits: 32, PCM: true, Channels: 1},
		{Op: OpAudio, SampleRate: 22050, Bits: 32, PCM: true},
		{Op: OpAudio, SampleRate: 22050, Bits: 32, PCM: true, Channels: 6},
		// Channels belong to the samples, not to FLAC.
		{Op: OpAudio, Bits: 16, Channels: 2},
	} {
		if err := request.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted the request", request)
		}
	}
}

func TestSamplesForAnalysisTakeABackgroundSlot(t *testing.T) {
	var got []Priority
	dial := func(ctx context.Context, op string) (*net.UnixConn, func(), error) {
		got = append(got, priorityOf(ctx, op))
		return nil, nil, ErrUnavailable
	}
	ctx := context.Background()
	_, _ = audio(ctx, dial, nil, AudioRequest{SampleRate: 22050, Bits: 32, PCM: true, Channels: 1})
	_, _ = audio(ctx, dial, nil, AudioRequest{Bits: 16})
	_, _ = audio(WithPriority(ctx, Interactive), dial, nil, AudioRequest{SampleRate: 22050, Bits: 32, PCM: true, Channels: 1})
	if !slices.Equal(got, []Priority{Background, Streaming, Interactive}) {
		t.Fatalf("priorities = %v, want background for samples, streaming for playback, and the caller's own", got)
	}
}

// raw sends message as it is and returns the worker's error.
func raw(t *testing.T, local Local, op string, message map[string]any, files ...*os.File) error {
	t.Helper()
	message["op"], message["ack"] = op, true
	response, _, err := send(context.Background(), local.dial, op, message, files)
	if err == nil {
		_ = response.Close()
	}
	return err
}

func TestEditingRequestsRefuseFieldsTheyDoNotKnow(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	local := Local{Tools: Tools{FFmpeg: script(t, "touch "+marker+"; cat")}}
	input := openFile(t, "video")
	for _, message := range []map[string]any{
		{"start": 0, "frames": 30, "offset": 0, "filter": "movie=/etc/passwd"},
		{"start": 0, "frames": 30, "offset": 0, "path": "/srv/a-nas/data"},
		{"start": 0, "frames": 30, "offset": 0, "args": []string{"-i", "/etc/shadow"}},
	} {
		if err := raw(t, local, OpSegment, message, input); !errors.Is(err, ErrInvalid) {
			t.Errorf("segment %v = %v", message, err)
		}
	}
	if err := raw(t, local, OpFrames, map[string]any{"times": []float64{1}, "format": "gray", "width": 16, "height": 16, "vf": "x"}, input); !errors.Is(err, ErrInvalid) {
		t.Errorf("frames with vf = %v", err)
	}
	song := openFile(t, "song")
	if err := raw(t, local, OpAssemble, map[string]any{"frames": 30, "created": "2026-08-04T10:30:00Z", "output": "/tmp/x.mp4"}, input, song); !errors.Is(err, ErrInvalid) {
		t.Errorf("assemble with output = %v", err)
	}
	// Assemble takes the segments and the song; one descriptor is refused.
	if err := raw(t, local, OpAssemble, map[string]any{"frames": 30, "created": "2026-08-04T10:30:00Z"}, input); !errors.Is(err, ErrInvalid) {
		t.Errorf("assemble with one descriptor = %v", err)
	}
	if err := raw(t, local, OpAudio, map[string]any{"pcm": true, "bits": 32, "sampleRate": 22050, "channels": 1, "filter": "x"}, song); !errors.Is(err, ErrInvalid) {
		t.Errorf("PCM with filter = %v", err)
	}
	// Over a limit: refused before FFmpeg starts.
	ctx := context.Background()
	_, err := local.Frames(ctx, input, FramesRequest{Format: FormatGray, Start: 0, End: 2000, Rate: 1, Width: 256, Height: 256}, func([]byte) error { return nil })
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("frames over 64 MiB = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("FFmpeg ran for a refused request")
	}
}

func TestFramesArriveOneByOneAndOverflowIsAnError(t *testing.T) {
	ctx := context.Background()
	// Each run prints one 16×16 gray frame.
	local := Local{Tools: Tools{FFmpeg: script(t, "head -c 256 /dev/zero")}}
	var got int
	result, err := local.Frames(ctx, openFile(t, "video"), FramesRequest{Format: FormatGray, Times: []float64{0, 2, 4}, Width: 16, Height: 16},
		func(frame []byte) error {
			if len(frame) != 256 {
				t.Errorf("frame of %d bytes", len(frame))
			}
			got++
			return nil
		})
	if err != nil || got != 3 || !slices.Equal(result.Times, []float64{0, 2, 4}) {
		t.Fatalf("Frames() = %+v, %d frames, %v", result, got, err)
	}
	// A run of frames that goes on past what the request allows is too
	// large, not cut short and not unreadable.
	flood := Local{Tools: Tools{FFmpeg: script(t, "head -c 2560 /dev/zero")}}
	_, err = flood.Frames(ctx, openFile(t, "video"), FramesRequest{Format: FormatGray, Start: 0, End: 1, Rate: 2, Width: 16, Height: 16},
		func([]byte) error { return nil })
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Frames() past the limit = %v", err)
	}
	// A frame of the wrong size is a failure.
	short := Local{Tools: Tools{FFmpeg: script(t, "head -c 100 /dev/zero")}}
	if _, err := short.Frames(ctx, openFile(t, "video"), FramesRequest{Format: FormatGray, Times: []float64{1}, Width: 16, Height: 16},
		func([]byte) error { return nil }); err == nil {
		t.Fatal("Frames() accepted a short gray frame")
	}
	// No frame even well before the instant: unreadable.
	empty := Local{Tools: Tools{FFmpeg: script(t, "true")}}
	if _, err := empty.Frames(ctx, openFile(t, "video"), FramesRequest{Format: FormatGray, Times: []float64{9}, Width: 16, Height: 16},
		func([]byte) error { return nil }); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Frames() with no frame = %v", err)
	}
	// An error from the caller stops the frames.
	stop := errors.New("enough")
	if _, err := local.Frames(ctx, openFile(t, "video"), FramesRequest{Format: FormatGray, Times: []float64{0, 2}, Width: 16, Height: 16},
		func([]byte) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("Frames() stopped by the caller = %v", err)
	}
}

func TestEditingRunsBelowPlaybackAndChecksItsLength(t *testing.T) {
	// The first command in the stand-in already runs at the intended nice.
	local := Local{Tools: Tools{FFmpeg: script(t, "awk '{print $19}' /proc/$$/stat")}}
	piece, err := local.Segment(context.Background(), openFile(t, "video"), SegmentRequest{Frames: 30})
	if err != nil {
		t.Fatalf("Segment() error = %v", err)
	}
	data, err := io.ReadAll(piece)
	_ = piece.Close()
	if err != nil || strings.TrimSpace(string(data)) != strconv.Itoa(backgroundNice) {
		t.Fatalf("segment ran at nice %q, %v", data, err)
	}
	if priorityOf(context.Background(), OpSegment) != Background || priorityOf(context.Background(), OpFrames) != Background {
		t.Fatal("editing does not default to background slots")
	}
}

func TestEditingDeadlineReportsFailed(t *testing.T) {
	for _, op := range []string{OpFrames, OpSegment, OpAssemble} {
		t.Run(op, func(t *testing.T) {
			worker := Tools{FFmpeg: script(t, "exec sleep 30")}
			client := fakeWorker(t, func(conn *net.UnixConn) {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				_ = Serve(ctx, conn, worker)
			})
			ctx := context.Background()
			video := openFile(t, "video")
			var err error
			switch op {
			case OpFrames:
				_, err = client.Frames(ctx, video, FramesRequest{Format: FormatGray, Times: []float64{0}, Width: 16, Height: 16}, func([]byte) error { return nil })
			case OpSegment:
				_, err = client.Segment(ctx, video, SegmentRequest{Frames: 30})
			case OpAssemble:
				_, err = client.Assemble(ctx, video, openFile(t, "song"), AssembleRequest{Frames: 30, Created: "2026-08-04T18:30:00Z"})
			}
			if !errors.Is(err, ErrFailed) || errors.Is(err, ErrUnreadable) {
				t.Fatalf("%s on worker deadline: %v", op, err)
			}
		})
	}
}

func TestBlurBatchFailures(t *testing.T) {
	for _, test := range []struct {
		name, blur string
		want       error
	}{
		{"worker failure", "cat >/dev/null; exit 1", ErrFailed},
		{"missing filter", "cat >/dev/null; echo \"No such filter: 'blurdetect'\" >&2; exit 1", ErrUnavailable},
		{"missing value", "cat >/dev/null", ErrFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := script(t, "case \"$*\" in *blurdetect*) "+test.blur+";; *) printf jpeg;; esac")
			local := Local{Tools: Tools{FFmpeg: worker}}
			request := FramesRequest{Format: FormatJPEG, Times: []float64{0, 1}, Width: 64, Quality: 3, Blur: true}
			_, err := local.Frames(context.Background(), openFile(t, "video"), request, func([]byte) error { return nil })
			if !errors.Is(err, test.want) || errors.Is(err, ErrUnreadable) {
				t.Fatalf("Frames(blur) error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestASegmentCutShortIsAnError(t *testing.T) {
	// A worker that announces more than it sends, as one that died sending.
	short := fakeWorker(t, func(conn *net.UnixConn) {
		var request Request
		file, err := readFrame(conn, &request)
		if err != nil {
			return
		}
		unixmsg.CloseFile(file)
		_ = writeFrame(conn, Response{OK: true, Size: 100}, nil)
		_, _ = conn.Write([]byte("only ten b"))
	})
	piece, err := short.Segment(context.Background(), openFile(t, "x"), SegmentRequest{Frames: 30})
	if err != nil {
		t.Fatalf("Segment() error = %v", err)
	}
	data, err := io.ReadAll(piece)
	_ = piece.Close()
	if !errors.Is(err, ErrInterrupted) || len(data) != 10 {
		t.Fatalf("a segment cut short = %d bytes, %v", len(data), err)
	}
}

// realSample makes a test video with FFmpeg, or skips.
func realSample(t *testing.T, tools Tools, name, source string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	output, err := exec.Command(tools.FFmpeg, append([]string{"-hide_banner", "-v", "error"}, append(strings.Fields(source), path)...)...).CombinedOutput()
	if err != nil {
		t.Skipf("FFmpeg cannot make %s: %v %s", name, err, output)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func probeJSON(t *testing.T, tools Tools, path string, args ...string) map[string]any {
	t.Helper()
	output, err := exec.Command(tools.FFprobe, append(append([]string{"-v", "error", "-of", "json"}, args...), path)...).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestRealFFmpegFrames takes frames with FFmpeg when it is installed.
func TestRealFFmpegFrames(t *testing.T) {
	tools, ok := LocalTools()
	if !ok {
		t.Skip("FFmpeg is not installed")
	}
	video := realSample(t, tools, "wide.mp4", "-f lavfi -i testsrc2=size=1280x720:rate=29.97:duration=4 -c:v libx264 -pix_fmt yuv420p")
	local := Local{Tools: tools}
	ctx := context.Background()
	var jpegs [][]byte
	result, err := local.Frames(ctx, video, FramesRequest{Format: FormatJPEG, Times: []float64{0.5, 1.5, 3.99}, Width: 512, Quality: 3, Blur: true},
		func(frame []byte) error { jpegs = append(jpegs, frame); return nil })
	if err != nil || len(jpegs) != 3 || len(result.Blur) != 3 || len(result.Times) != 3 {
		t.Fatalf("Frames(jpeg) = %+v, %d frames, %v", result, len(jpegs), err)
	}
	for i, frame := range jpegs {
		if !bytes.HasPrefix(frame, []byte{0xff, 0xd8}) || result.Blur[i] == nil || *result.Blur[i] <= 0 {
			t.Fatalf("frame %d: %d bytes, blur %v", i, len(frame), result.Blur[i])
		}
	}
	// A black frame has no blur value: it is black, not an unreadable file.
	black := realSample(t, tools, "black.mp4", "-f lavfi -i color=black:size=1280x720:rate=30:duration=2 -c:v libx264 -pix_fmt yuv420p")
	result, err = local.Frames(ctx, black, FramesRequest{Format: FormatJPEG, Times: []float64{1}, Width: 512, Quality: 3, Blur: true},
		func([]byte) error { return nil })
	if err != nil || len(result.Blur) != 1 || result.Blur[0] != nil {
		t.Fatalf("Frames(black) = %+v, %v", result, err)
	}
	if trailer, _ := json.Marshal(result); !strings.Contains(string(trailer), `"blur":[null]`) {
		t.Fatalf("a black frame's blur is sent as %s", trailer)
	}
	// Past the last frame the worker steps back rather than fail.
	var late int
	result, err = local.Frames(ctx, video, FramesRequest{Format: FormatGray, Times: []float64{4.5}, Width: 64, Height: 36},
		func([]byte) error { late++; return nil })
	if err != nil || late != 1 || result.Times[0] >= 4.5 {
		t.Fatalf("Frames() after the end = %+v, %d, %v", result, late, err)
	}
	var grays int
	_, err = local.Frames(ctx, video, FramesRequest{Format: FormatGray, Start: 1, End: 2, Rate: 10, Width: 320, Height: 180},
		func(frame []byte) error {
			if len(frame) != 320*180 {
				t.Errorf("gray frame of %d bytes", len(frame))
			}
			grays++
			return nil
		})
	if err != nil || grays < 10 || grays > 11 {
		t.Fatalf("Frames(gray range) = %d frames, %v", grays, err)
	}
	var parts int
	result, err = local.Frames(ctx, video, FramesRequest{Format: FormatJPEG, Start: 0, End: 4, Rate: 2, LongEdge: 256, Quality: 5, Blur: true},
		func(frame []byte) error {
			if !bytes.HasPrefix(frame, []byte{0xff, 0xd8}) || !bytes.HasSuffix(frame, []byte{0xff, 0xd9}) {
				t.Errorf("JPEG part of %d bytes", len(frame))
			}
			parts++
			return nil
		})
	if err != nil || parts < 8 || parts > 9 || len(result.Blur) != parts {
		t.Fatalf("Frames(jpeg range) = %d frames, %d blur values, %v", parts, len(result.Blur), err)
	}
}

// TestRealFFmpegFilm renders segments, joins them byte by byte and adds a
// song, with FFmpeg when it is installed.
func TestRealFFmpegFilm(t *testing.T) {
	tools, ok := LocalTools()
	if !ok {
		t.Skip("FFmpeg is not installed")
	}
	wide := realSample(t, tools, "wide.mp4", "-f lavfi -i testsrc2=size=1280x720:rate=29.97:duration=8 -c:v libx264 -pix_fmt yuv420p -g 60")
	tall := realSample(t, tools, "tall.mp4", "-f lavfi -i testsrc2=size=720x1280:rate=30:duration=5 -c:v libx264 -pix_fmt yuv420p")
	song := realSample(t, tools, "song.mp3", "-f lavfi -i sine=frequency=440:duration=30 -ac 2 -c:a libmp3lame")
	local := Local{Tools: tools}
	ctx := context.Background()
	joined, err := os.Create(filepath.Join(t.TempDir(), "joined.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = joined.Close() }()
	offset := 0
	for _, piece := range []struct {
		file    *os.File
		request SegmentRequest
	}{
		{wide, SegmentRequest{Start: 1, Frames: 75}},
		{tall, SegmentRequest{Start: 0.5, Frames: 90, Blurred: true}},
		{wide, SegmentRequest{Start: 6.5, Frames: 60, Slow: 2.5}},
		// Half a second of source left for a second and a half: the last
		// frame repeats.
		{tall, SegmentRequest{Start: 4.5, Frames: 45, FadeFrames: 15}},
	} {
		piece.request.Offset = offset
		body, err := local.Segment(ctx, piece.file, piece.request)
		if err != nil {
			t.Fatalf("Segment(%+v) error = %v", piece.request, err)
		}
		if _, err := io.Copy(joined, body); err != nil {
			t.Fatal(err)
		}
		_ = body.Close()
		offset += piece.request.Frames
	}
	film, err := local.Assemble(ctx, joined, song, AssembleRequest{SongStart: 3, Frames: offset, Created: "2026-08-04T18:30:00+08:00"})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "film.mp4")
	data, err := io.ReadAll(film)
	_ = film.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []AssembleRequest{
		{SongStart: 3, Frames: offset + 30, Created: "2026-08-04T18:30:00Z"},
		{SongStart: 29, Frames: offset, Created: "2026-08-04T18:30:00Z"},
	} {
		body, err := local.Assemble(ctx, joined, song, request)
		if body != nil {
			_ = body.Close()
		}
		if !errors.Is(err, ErrFailed) {
			t.Fatalf("Assemble(%+v) with incomplete input = %v, want ErrFailed", request, err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if moov, mdat := bytes.Index(data, []byte("moov")), bytes.Index(data, []byte("mdat")); moov < 0 || moov > mdat {
		t.Fatalf("the film's index is not first: moov %d, mdat %d", moov, mdat)
	}
	info := probeJSON(t, tools, path, "-count_frames", "-show_entries", "stream=codec_type,width,height,nb_read_frames,r_frame_rate:format=duration:format_tags=creation_time")
	format := info["format"].(map[string]any)
	duration, _ := strconv.ParseFloat(format["duration"].(string), 64)
	if want := float64(offset) / FilmRate; duration < want-0.1 || duration > want+0.1 {
		t.Errorf("film is %.3f s, want %.3f", duration, want)
	}
	if created := format["tags"].(map[string]any)["creation_time"]; created != "2026-08-04T10:30:00.000000Z" {
		t.Errorf("creation_time = %v", created)
	}
	var audio bool
	for _, entry := range info["streams"].([]any) {
		stream := entry.(map[string]any)
		switch stream["codec_type"] {
		case "video":
			if stream["width"] != float64(FilmWidth) || stream["height"] != float64(FilmHeight) || stream["r_frame_rate"] != "30/1" ||
				stream["nb_read_frames"] != strconv.Itoa(offset) {
				t.Errorf("video stream = %v, want %d frames", stream, offset)
			}
		case "audio":
			audio = true
		}
	}
	if !audio {
		t.Error("the film has no sound")
	}
	// The tall picture set on its blurred self has no black bars; the same
	// picture fitted on black has them. Frame 120 is in the blurred piece,
	// frame 240 in the fitted one.
	corner := func(second float64) byte {
		t.Helper()
		pixel, err := exec.Command(tools.FFmpeg, "-v", "error", "-i", path, "-ss", seconds(second), "-frames:v", "1",
			"-vf", "crop=40:40:0:0,format=gray,scale=1:1", "-f", "rawvideo", "-").Output()
		if err != nil || len(pixel) != 1 {
			t.Fatalf("corner at %.1f s: %v %v", second, pixel, err)
		}
		return pixel[0]
	}
	if blurredCorner, blackCorner := corner(4), corner(8); blurredCorner < 24 || blackCorner > 8 {
		t.Errorf("corners: blurred background %d, black bars %d", blurredCorner, blackCorner)
	}
	// The timestamps run on: every frame is 1/30 s after the one before,
	// across the joins.
	output, err := exec.Command(tools.FFprobe, "-v", "error", "-select_streams", "v", "-show_entries", "frame=pts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	previous := -1.0
	for i, line := range strings.Fields(string(output)) {
		at, _ := strconv.ParseFloat(strings.TrimSuffix(line, ","), 64)
		if i > 0 && (at-previous < 0.033 || at-previous > 0.0337) {
			t.Fatalf("frame %d at %.4f s follows %.4f s", i, at, previous)
		}
		previous = at
	}
}

// TestRealFFmpegSamples checks that the raw samples are those music analysis
// reads: mono 22,050 Hz as the music probe (P2) decoded them for beat_this,
// and stereo 44.1 kHz for allin1, both FFmpeg's own float output.
func TestRealFFmpegSamples(t *testing.T) {
	tools, ok := LocalTools()
	if !ok {
		t.Skip("FFmpeg is not installed")
	}
	song := realSample(t, tools, "song.flac", "-f lavfi -i sine=frequency=440:duration=3:sample_rate=48000 -f lavfi -i sine=frequency=660:duration=3:sample_rate=48000 -filter_complex [0][1]amerge=inputs=2 -c:a flac")
	for _, request := range []AudioRequest{
		{SampleRate: 22050, Bits: 32, PCM: true, Channels: 1},
		{SampleRate: 44100, Bits: 32, PCM: true, Channels: 2},
	} {
		samples, err := (Local{Tools: tools}).Audio(context.Background(), song, request)
		if err != nil {
			t.Fatalf("Audio(%+v) error = %v", request, err)
		}
		got, err := io.ReadAll(samples)
		_ = samples.Close()
		if err != nil {
			t.Fatal(err)
		}
		// The probe's command (ai/eval/montage_music.py, decode).
		want, err := exec.Command(tools.FFmpeg, "-v", "error", "-i", song.Name(), "-ac", strconv.Itoa(request.Channels),
			"-ar", strconv.Itoa(request.SampleRate), "-f", "f32le", "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		if least := 3*request.SampleRate*request.Channels*4 - 4096; len(got) < least || !bytes.Equal(got, want) {
			t.Fatalf("PCM samples %+v: %d bytes, the analysis command gives %d (equal %v)", request, len(got), len(want), bytes.Equal(got, want))
		}
	}
}
