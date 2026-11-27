package ffmpegworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
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
// SampleRate (0 keeps the source's) with Bits (16 or 24) per sample. With
// PCM it decodes to raw little-endian float samples instead, with Bits 32,
// Channels 1 (mixed down) or 2, interleaved, at SampleRate (8 to 48 kHz):
// what music analysis reads. That is background work: it takes a background
// slot unless ctx says otherwise, and FFmpeg runs below playback.
type AudioRequest struct {
	Start      float64
	SampleRate int
	Bits       int
	PCM        bool
	Channels   int
}

// Client reaches the sandboxed worker through its socket; systemd starts a
// worker for each connection. Slots names the slot directory shared by every
// process that uses the socket; without it the slots are this process's own.
type Client struct {
	Socket string
	Slots  string
}

// Local runs the worker's server in this process, for development without
// the systemd units. It runs FFmpeg with the Product Service's identity.
type Local struct {
	Tools Tools
	// slots, when set, are the slots this Local takes in place of the
	// process's own; tests give theirs shorter waits.
	slots *admission
}

var (
	_ Worker  = Client{}
	_ Worker  = Local{}
	_ Editing = Client{}
	_ Editing = Local{}
)

// dialer takes a slot for a request of op and connects to a worker; release
// gives the slot back.
type dialer func(ctx context.Context, op string) (conn *net.UnixConn, release func(), err error)

func (c Client) dial(ctx context.Context, op string) (*net.UnixConn, func(), error) {
	release, err := admit(ctx, c.Slots, priorityOf(ctx, op))
	if err != nil {
		return nil, nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return conn.(*net.UnixConn), release, nil
}

func (l Local) dial(ctx context.Context, op string) (*net.UnixConn, func(), error) {
	var release func()
	var err error
	if l.slots != nil {
		release, err = l.slots.acquire(ctx, priorityOf(ctx, op))
	} else {
		release, err = admit(ctx, "", priorityOf(ctx, op))
	}
	if err != nil {
		return nil, nil, err
	}
	conn, err := l.connect(ctx)
	if err != nil {
		release()
		return nil, nil, err
	}
	return conn, release, nil
}

func (l Local) connect(ctx context.Context) (*net.UnixConn, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	conns := make([]*net.UnixConn, 2)
	for i, descriptor := range pair {
		file := os.NewFile(uintptr(descriptor), "ffmpeg-worker")
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

// body is a response body; closing it closes the connection and frees its
// slot. Once the request's context ends, reads report why.
type body struct {
	*net.UnixConn
	ctx  context.Context
	stop func() bool
	done func()
}

func (b body) Read(p []byte) (int, error) {
	n, err := b.UnixConn.Read(p)
	if err != nil && b.ctx.Err() != nil {
		return n, b.ctx.Err()
	}
	return n, err
}

func (b body) Close() error {
	b.stop()
	err := b.UnixConn.Close()
	b.done()
	return err
}

// call takes a slot, sends request with file attached and returns the body
// after an OK response. Cancelling ctx closes the connection. Until a worker
// acknowledges the request a failure is ErrUnavailable, as when systemd
// drops a connection over its limit; after that it is ErrInterrupted.
func call(ctx context.Context, dial dialer, request Request, file *os.File) (io.ReadCloser, error) {
	request.Ack = true
	response, _, err := send(ctx, dial, request.Op, request, []*os.File{file})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// send is call for any request: message must ask for an acknowledgement.
// It returns the open body and the response that preceded it.
func send(ctx context.Context, dial dialer, op string, message any, files []*os.File) (body, Response, error) {
	if err := ReadOnly(files...); err != nil {
		return body{}, Response{}, err
	}
	conn, release, err := dial(ctx, op)
	if err != nil {
		return body{}, Response{}, err
	}
	ctx, cancel := working(ctx)
	done := func() { cancel(); release() }
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	fail := func(err error) (body, Response, error) {
		stop()
		_ = conn.Close()
		done()
		return body{}, Response{}, err
	}
	if err := unixmsg.WriteFiles(conn, message, files, maxFrame); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrUnavailable, err))
	}
	var response Response
	accepted := false
	for {
		if _, err := readFrame(conn, &response); err != nil {
			switch {
			case ctx.Err() != nil:
				return fail(ctx.Err())
			case accepted:
				return fail(fmt.Errorf("%w: %v", ErrInterrupted, err))
			}
			return fail(fmt.Errorf("%w: %v", ErrUnavailable, err))
		}
		// A worker from before acknowledgements answers straight away.
		if !response.Accepted || accepted {
			break
		}
		accepted, response = true, Response{}
	}
	if !response.OK {
		return fail(responseError(response))
	}
	return body{UnixConn: conn, ctx: ctx, stop: stop, done: done}, response, nil
}

// ReadOnly refuses an input that could write, as every request does before
// it takes a slot: ADR 0017 hands the worker read-only descriptors, so a
// decoder taken over by a malicious file can only read what it was given.
func ReadOnly(files ...*os.File) error {
	for _, file := range files {
		// A nil file has no name for the errors below to print.
		if file == nil {
			return errors.New("ffmpeg worker input is missing")
		}
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil {
			return fmt.Errorf("ffmpeg worker input %s: %w", file.Name(), err)
		}
		if flags&unix.O_ACCMODE != unix.O_RDONLY {
			return fmt.Errorf("ffmpeg worker input %s is open for writing", file.Name())
		}
	}
	return nil
}

// responseError is the error a response that is not OK reports.
func responseError(response Response) error {
	switch response.Code {
	case CodeUnreadable:
		return fmt.Errorf("%w: %s", ErrUnreadable, response.Message)
	case CodeUnavailable:
		return fmt.Errorf("%w: %s", ErrUnavailable, response.Message)
	case CodeInvalid:
		return fmt.Errorf("%w: %s", ErrInvalid, response.Message)
	case CodeTooLarge:
		return fmt.Errorf("%w: %s", ErrTooLarge, response.Message)
	}
	return fmt.Errorf("%w: %s", ErrFailed, response.Message)
}

func readAll(ctx context.Context, dial dialer, request Request, file *os.File) ([]byte, error) {
	response, err := call(ctx, dial, request, file)
	if err != nil {
		return nil, err
	}
	defer response.Close()
	data, err := io.ReadAll(io.LimitReader(response, maxOutput[request.Op]+1))
	switch {
	case err == nil:
		return data, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, err
	}
	return nil, fmt.Errorf("%w: %v", ErrInterrupted, err)
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
	request := Request{Op: OpAudio, Start: options.Start, SampleRate: options.SampleRate, Bits: options.Bits, PCM: options.PCM, Channels: options.Channels, Stream: -1, AudioTrack: -1}
	if _, marked := ctx.Value(priorityKey{}).(Priority); options.PCM && !marked {
		ctx = WithPriority(ctx, Background)
	}
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
