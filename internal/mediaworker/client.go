package mediaworker

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// Worker runs typed media operations on files that are already open.
// The caller keeps ownership of file and may close it once a call returns;
// a stream keeps its own descriptor.
type Worker interface {
	Probe(ctx context.Context, file *os.File) (Probe, error)
	Frame(ctx context.Context, file *os.File, at float64, width int) ([]byte, error)
	Image(ctx context.Context, file *os.File, size int) ([]byte, error)
	Subtitle(ctx context.Context, file *os.File, request SubtitleRequest) ([]byte, error)
	Stream(ctx context.Context, file *os.File, request StreamRequest) (io.ReadCloser, error)
	Audio(ctx context.Context, file *os.File, request AudioRequest) (io.ReadCloser, error)
}

// SubtitleRequest converts an external subtitle file in Format (srt, ass or
// webvtt) and Charset, or the embedded Stream, to WebVTT.
type SubtitleRequest struct {
	Format  string
	Charset string
	Stream  int
}

// StreamRequest converts a video to fragmented MP4 from Start.
type StreamRequest struct {
	Start      float64
	CopyVideo  bool
	HEVC       bool
	Height     int
	MaxBitrate int
	// AudioTrack is the position among the audio streams, or -1 for none.
	AudioTrack int
	CopyAudio  bool
}

// AudioRequest decodes the first audio stream to FLAC from Start, at
// SampleRate (0 keeps the source's) with Bits (16 or 24) per sample.
type AudioRequest struct {
	Start      float64
	SampleRate int
	Bits       int
}

// SpeechRequest decodes the first audio stream to 16 kHz mono in Format
// (SpeechOpus or SpeechPCM) from Start for Duration seconds, 0 meaning to
// the end.
type SpeechRequest struct {
	Format   string
	Start    float64
	Duration float64
}

// Speaker decodes audio for speech services. It is not part of Worker, so the
// apps' test workers need not implement it.
type Speaker interface {
	Speech(ctx context.Context, file *os.File, request SpeechRequest) (io.ReadCloser, error)
}

var (
	_ Speaker = Client{}
	_ Speaker = Local{}
)

// Client reaches the sandboxed worker through its socket; systemd starts a
// worker for each connection.
type Client struct {
	Socket string
}

// Local runs the worker's server in this process, for development without
// the systemd units. It runs FFmpeg with the Product Service's identity.
type Local struct {
	Tools Tools
}

var (
	_ Worker = Client{}
	_ Worker = Local{}
)

type dialer func(context.Context) (*net.UnixConn, error)

func (c Client) dial(ctx context.Context) (*net.UnixConn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return conn.(*net.UnixConn), nil
}

func (l Local) dial(ctx context.Context) (*net.UnixConn, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	conns := make([]*net.UnixConn, 2)
	for i, descriptor := range pair {
		file := os.NewFile(uintptr(descriptor), "media-worker")
		conn, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			for _, opened := range conns[:i] {
				_ = opened.Close()
			}
			if i == 0 {
				_ = unix.Close(pair[1])
			}
			return nil, err
		}
		conns[i] = conn.(*net.UnixConn)
	}
	go func() { _ = Serve(context.WithoutCancel(ctx), conns[1], l.Tools) }()
	return conns[0], nil
}

// body is a response body; closing it closes the connection.
type body struct {
	*net.UnixConn
	stop func() bool
}

func (b body) Close() error {
	b.stop()
	return b.UnixConn.Close()
}

// call sends request with file attached and returns the body after an OK
// response. Cancelling ctx closes the connection.
func call(ctx context.Context, dial dialer, request Request, file *os.File) (io.ReadCloser, error) {
	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	if err := writeFrame(conn, request, file); err != nil {
		stop()
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var response Response
	if _, err := readFrame(conn, &response); err != nil {
		stop()
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !response.OK {
		stop()
		_ = conn.Close()
		switch response.Code {
		case CodeUnreadable:
			return nil, fmt.Errorf("%w: %s", ErrUnreadable, response.Message)
		case CodeUnavailable:
			return nil, fmt.Errorf("%w: %s", ErrUnavailable, response.Message)
		case CodeInvalid:
			return nil, fmt.Errorf("%w: %s", ErrInvalid, response.Message)
		}
		return nil, fmt.Errorf("media worker: %s", response.Message)
	}
	return body{UnixConn: conn, stop: stop}, nil
}

func readAll(ctx context.Context, dial dialer, request Request, file *os.File) ([]byte, error) {
	response, err := call(ctx, dial, request, file)
	if err != nil {
		return nil, err
	}
	defer response.Close()
	data, err := io.ReadAll(io.LimitReader(response, maxOutput[request.Op]+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return data, nil
}

func probe(ctx context.Context, dial dialer, file *os.File) (Probe, error) {
	data, err := readAll(ctx, dial, Request{Op: OpProbe, Stream: -1, AudioTrack: -1}, file)
	if err != nil {
		return Probe{}, err
	}
	result, err := ParseProbe(data)
	if err != nil {
		return Probe{}, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	return result, nil
}

func frame(ctx context.Context, dial dialer, file *os.File, at float64, width int) ([]byte, error) {
	return readAll(ctx, dial, Request{Op: OpFrame, At: at, Width: width, Stream: -1, AudioTrack: -1}, file)
}

func subtitle(ctx context.Context, dial dialer, file *os.File, source SubtitleRequest) ([]byte, error) {
	return readAll(ctx, dial, Request{Op: OpSubtitle, Format: source.Format, Charset: source.Charset, Stream: source.Stream, AudioTrack: -1}, file)
}

func stream(ctx context.Context, dial dialer, file *os.File, options StreamRequest) (io.ReadCloser, error) {
	request := Request{
		Op: OpStream, Start: options.Start, CopyVideo: options.CopyVideo, HEVC: options.HEVC, Height: options.Height,
		MaxBitrate: options.MaxBitrate, AudioTrack: options.AudioTrack, CopyAudio: options.CopyAudio, Stream: -1,
	}
	return call(ctx, dial, request, file)
}

func audio(ctx context.Context, dial dialer, file *os.File, options AudioRequest) (io.ReadCloser, error) {
	request := Request{Op: OpAudio, Start: options.Start, SampleRate: options.SampleRate, Bits: options.Bits, Stream: -1, AudioTrack: -1}
	return call(ctx, dial, request, file)
}

func speech(ctx context.Context, dial dialer, file *os.File, options SpeechRequest) (io.ReadCloser, error) {
	request := Request{Op: OpSpeech, Format: options.Format, Start: options.Start, Duration: options.Duration, Stream: -1, AudioTrack: -1}
	return call(ctx, dial, request, file)
}

func (c Client) Probe(ctx context.Context, file *os.File) (Probe, error) {
	return probe(ctx, c.dial, file)
}
func (c Client) Frame(ctx context.Context, file *os.File, at float64, width int) ([]byte, error) {
	return frame(ctx, c.dial, file, at, width)
}
func (c Client) Image(ctx context.Context, file *os.File, size int) ([]byte, error) {
	return readAll(ctx, c.dial, Request{Op: OpImage, Width: size}, file)
}
func (c Client) Subtitle(ctx context.Context, file *os.File, source SubtitleRequest) ([]byte, error) {
	return subtitle(ctx, c.dial, file, source)
}
func (c Client) Stream(ctx context.Context, file *os.File, options StreamRequest) (io.ReadCloser, error) {
	return stream(ctx, c.dial, file, options)
}
func (c Client) Audio(ctx context.Context, file *os.File, options AudioRequest) (io.ReadCloser, error) {
	return audio(ctx, c.dial, file, options)
}

func (c Client) Speech(ctx context.Context, file *os.File, options SpeechRequest) (io.ReadCloser, error) {
	return speech(ctx, c.dial, file, options)
}

func (l Local) Probe(ctx context.Context, file *os.File) (Probe, error) {
	return probe(ctx, l.dial, file)
}
func (l Local) Frame(ctx context.Context, file *os.File, at float64, width int) ([]byte, error) {
	return frame(ctx, l.dial, file, at, width)
}
func (l Local) Image(ctx context.Context, file *os.File, size int) ([]byte, error) {
	return readAll(ctx, l.dial, Request{Op: OpImage, Width: size}, file)
}
func (l Local) Subtitle(ctx context.Context, file *os.File, source SubtitleRequest) ([]byte, error) {
	return subtitle(ctx, l.dial, file, source)
}
func (l Local) Stream(ctx context.Context, file *os.File, options StreamRequest) (io.ReadCloser, error) {
	return stream(ctx, l.dial, file, options)
}
func (l Local) Audio(ctx context.Context, file *os.File, options AudioRequest) (io.ReadCloser, error) {
	return audio(ctx, l.dial, file, options)
}

func (l Local) Speech(ctx context.Context, file *os.File, options SpeechRequest) (io.ReadCloser, error) {
	return speech(ctx, l.dial, file, options)
}

// LocalTools finds FFmpeg on PATH, or returns false.
func LocalTools() (Tools, bool) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return Tools{}, false
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return Tools{}, false
	}
	heif, _ := exec.LookPath("heif-thumbnailer")
	return Tools{FFmpeg: ffmpeg, FFprobe: ffprobe, HEIFThumbnailer: heif}, true
}
