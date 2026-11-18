package ffmpegworker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// The editing operations take frames of a video to look at, render pieces of
// a film and join them with a song (docs/architecture/montage-center.md).
// Their requests are decoded strictly: a field the worker does not know, a
// path or a filter, is refused. Every command is built here from bounded
// numbers. They run as background work: FFmpeg at a lower priority than
// playback and with a few threads, so a render never halves the CPU a
// transcode someone is watching gets.

// Frame formats.
const (
	FormatJPEG = "jpeg"
	FormatGray = "gray"
)

// The film: every segment and the film itself are 1920×1080 at 30 frames
// a second.
const (
	FilmRate   = 30
	FilmWidth  = 1920
	FilmHeight = 1080
)

const (
	maxFrames      = 1200
	maxJPEGFrames  = 600
	maxGrayBytes   = 64 << 20
	maxFrameBytes  = 16 << 20 // one frame: a 3840×3840 gray frame is 14 MiB
	maxFrameSide   = 3840
	maxJPEGSide    = 1920
	minJPEGSide    = 64
	maxSegment     = 12 * FilmRate  // frames of output
	maxFilm        = 150 * FilmRate // frames of output
	maxSegmentSize = 48 << 20
	maxFilmSize    = 512 << 20
	slowest        = 2.5
	filmFadeIn     = 0.3
	filmFadeOut    = 2.5
	editThreads    = "4"
	// backgroundNice lowers FFmpeg below playback; raising one's own nice
	// value needs no privilege.
	backgroundNice = 10
	segmentTimeout = 5 * time.Minute
	filmTimeout    = 10 * time.Minute
	blurTimeout    = 30 * time.Second
	// boundary separates the JPEG frames of a run of frames.
	boundary = "anas-frame"
)

// FramesRequest takes frames of a video, either at each of Times (seconds
// from the start, increasing) or Rate a second from Start to End.
//
// JPEG frames are Width wide, or have LongEdge on their longer side and are
// never enlarged, encoded at FFmpeg's Quality (2 is best, 31 worst); Blur
// also measures each encoded JPEG with blurdetect. Gray frames are Width ×
// Height bytes, one per pixel, scaled to exactly that size: the caller
// keeps the picture's shape. Turned turns the picture half way round, for a
// camera held upside down.
type FramesRequest struct {
	Times    []float64 `json:"times,omitempty"`
	Start    float64   `json:"start,omitempty"`
	End      float64   `json:"end,omitempty"`
	Rate     float64   `json:"rate,omitempty"`
	Format   string    `json:"format"`
	Width    int       `json:"width,omitempty"`
	Height   int       `json:"height,omitempty"`
	LongEdge int       `json:"longEdge,omitempty"`
	Quality  int       `json:"quality,omitempty"`
	Blur     bool      `json:"blur,omitempty"`
	Turned   bool      `json:"turned,omitempty"`
}

// FramesResult describes the frames once all of them came. Times are the
// instants actually used, in the order of the request's Times: near the end
// of a video there may be no frame at the instant asked for, and the worker
// takes an earlier one. Blur holds each JPEG's blurdetect value when the
// request asked for it, nil (null) where blurdetect measures nothing: a flat
// picture such as a black frame, which the prototype's quality check calls
// black rather than sharp or blurred.
type FramesResult struct {
	Times []float64  `json:"times,omitempty"`
	Blur  []*float64 `json:"blur,omitempty"`
}

// SegmentRequest renders Frames frames (at FilmRate) of a video from Start
// seconds as MPEG-TS, H.264 1920×1080 without sound. Slow (1 to 2.5)
// stretches the source, so it reads Frames/FilmRate/Slow seconds of it;
// Blurred sets a picture that is not 16:9 on a blurred copy of itself
// rather than black bars; FadeFrames fade the last frames to black. Offset
// is the frame at which the segment starts in the film: the timestamps
// carry it, so segments written one after another play continuously.
type SegmentRequest struct {
	Start      float64 `json:"start"`
	Frames     int     `json:"frames"`
	Slow       float64 `json:"slow,omitempty"`
	Turned     bool    `json:"turned,omitempty"`
	Blurred    bool    `json:"blurred,omitempty"`
	FadeFrames int     `json:"fadeFrames,omitempty"`
	Offset     int     `json:"offset"`
}

// AssembleRequest makes the film: the segments' pictures copied as they are,
// Frames frames long, with the song from SongStart seconds, faded in and out,
// as an MP4 that starts playing before it has all loaded. Created is when it
// was taken, in RFC 3339, written as the MP4's creation_time.
type AssembleRequest struct {
	SongStart float64 `json:"songStart"`
	Frames    int     `json:"frames"`
	Created   string  `json:"created"`
}

// The requests on the wire carry the operation and the acknowledgement.
type (
	framesWire struct {
		Op  string `json:"op"`
		Ack bool   `json:"ack,omitempty"`
		FramesRequest
	}
	segmentWire struct {
		Op  string `json:"op"`
		Ack bool   `json:"ack,omitempty"`
		SegmentRequest
	}
	assembleWire struct {
		Op  string `json:"op"`
		Ack bool   `json:"ack,omitempty"`
		AssembleRequest
	}
	// framesTrailer follows the frames: whether they all came, and what
	// the worker measured.
	framesTrailer struct {
		Response
		FramesResult
	}
)

func isEditing(op string) bool { return op == OpFrames || op == OpSegment || op == OpAssemble }

// count checks the request against every limit before FFmpeg starts and
// returns how many frames it may produce.
func (r FramesRequest) count() (int, error) {
	invalid := func(why string) (int, error) { return 0, fmt.Errorf("invalid frames request: %s", why) }
	var count int
	if len(r.Times) > 0 {
		if r.Start != 0 || r.End != 0 || r.Rate != 0 {
			return invalid("times and a range together")
		}
		if len(r.Times) > maxFrames {
			return invalid("too many frames")
		}
		for i, at := range r.Times {
			if !finiteIn(at, 0, 1e6) || i > 0 && at <= r.Times[i-1] {
				return invalid("times must increase")
			}
		}
		count = len(r.Times)
	} else {
		if !finiteIn(r.Start, 0, 1e6) || !finiteIn(r.End, 0, 1e6) || r.End <= r.Start || !finiteIn(r.Rate, 0.01, FilmRate) {
			return invalid("bad range")
		}
		// The fps filter gives about (End-Start)×Rate frames; one more covers
		// where its rounding lands.
		estimate := math.Ceil((r.End-r.Start)*r.Rate) + 1
		if estimate > maxFrames {
			return invalid("too many frames")
		}
		count = int(estimate)
	}
	switch r.Format {
	case FormatJPEG:
		side := max(r.Width, r.LongEdge)
		if r.Height != 0 || (r.Width == 0) == (r.LongEdge == 0) || side < minJPEGSide || side > maxJPEGSide {
			return invalid("bad JPEG size")
		}
		if r.Quality < 2 || r.Quality > 31 {
			return invalid("bad JPEG quality")
		}
		if count > maxJPEGFrames {
			return invalid("too many JPEG frames")
		}
	case FormatGray:
		if r.LongEdge != 0 || r.Quality != 0 || r.Blur {
			return invalid("JPEG options on gray frames")
		}
		if r.Width < 16 || r.Width > maxFrameSide || r.Height < 16 || r.Height > maxFrameSide {
			return invalid("bad gray size")
		}
		if count*r.Width*r.Height > maxGrayBytes {
			return invalid("too many gray bytes")
		}
	default:
		return invalid("unknown format")
	}
	return count, nil
}

// timeout bounds a frames request by the length of video it covers: a
// minute, and three seconds for each second of video, at most half an
// hour. Each instant of Times counts a second, for the decoding from the
// keyframe before it.
func (r FramesRequest) timeout() time.Duration {
	covered := r.End - r.Start
	if len(r.Times) > 0 {
		covered = r.Times[len(r.Times)-1] - r.Times[0] + float64(len(r.Times))
	}
	return min(time.Minute+time.Duration(3*covered*float64(time.Second)), 30*time.Minute)
}

func (r SegmentRequest) validate() error {
	if !finiteIn(r.Start, 0, 1e6) || r.Frames < 1 || r.Frames > maxSegment || r.Slow != 0 && !finiteIn(r.Slow, 1, slowest) ||
		r.FadeFrames < 0 || r.FadeFrames > r.Frames || r.Offset < 0 || r.Offset+r.Frames > maxFilm {
		return errors.New("invalid segment request")
	}
	return nil
}

func (r AssembleRequest) validate() (time.Time, error) {
	created, err := time.Parse(time.RFC3339, r.Created)
	if err != nil || !finiteIn(r.SongStart, 0, 1e6) || r.Frames < 1 || r.Frames > maxFilm {
		return time.Time{}, errors.New("invalid assemble request")
	}
	return created, nil
}

// decodeStrict decodes an editing request, refusing unknown fields and
// anything after the object.
func decodeStrict(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("data after the request")
	}
	return nil
}

func quiet() []string { return []string{"-nostdin", "-hide_banner", "-v", "error"} }

func turn(turned bool) string {
	if turned {
		return "hflip,vflip,"
	}
	return ""
}

// scaled is the scale filter for a frames request.
func (r FramesRequest) scaled() string {
	switch {
	case r.Format == FormatGray:
		return fmt.Sprintf("scale=%d:%d,format=gray", r.Width, r.Height)
	case r.Width > 0:
		return fmt.Sprintf("scale=%d:-2", r.Width)
	}
	edge := strconv.Itoa(r.LongEdge)
	return "scale='if(gte(iw,ih),min(" + edge + ",iw),-2)':'if(gte(iw,ih),-2,min(" + edge + ",ih))'"
}

// frameCommand takes the first frame at or after at.
func (t Tools) frameCommand(r FramesRequest, at float64) (string, []string) {
	args := append(quiet(), "-threads", editThreads)
	if at > 0 {
		args = append(args, "-ss", seconds(at))
	}
	args = append(args, "-i", "fd:", "-map", "0:v:0", "-an", "-sn", "-dn", "-frames:v", "1", "-vf", turn(r.Turned)+r.scaled())
	if r.Format == FormatGray {
		return t.FFmpeg, append(args, "-f", "rawvideo", "pipe:1")
	}
	return t.FFmpeg, append(args, "-q:v", strconv.Itoa(r.Quality), "-c:v", "mjpeg", "-f", "image2", "pipe:1")
}

// rangeCommand takes Rate frames a second from Start to End in one run.
func (t Tools) rangeCommand(r FramesRequest) (string, []string) {
	args := append(quiet(), "-threads", editThreads)
	if r.Start > 0 {
		args = append(args, "-ss", seconds(r.Start))
	}
	args = append(args, "-t", seconds(r.End-r.Start), "-i", "fd:", "-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", turn(r.Turned)+"fps="+strconv.FormatFloat(r.Rate, 'f', -1, 64)+","+r.scaled())
	if r.Format == FormatGray {
		return t.FFmpeg, append(args, "-f", "rawvideo", "pipe:1")
	}
	return t.FFmpeg, append(args, "-q:v", strconv.Itoa(r.Quality), "-c:v", "mjpeg", "-f", "mpjpeg", "-boundary_tag", boundary, "pipe:1")
}

// blurCommand measures one encoded JPEG on standard input, as the cards were
// calibrated: on the JPEG, not on the decoded frame.
func (t Tools) blurCommand() (string, []string) {
	return t.FFmpeg, append(quiet(), "-f", "jpeg_pipe", "-i", "fd:", "-vf", "blurdetect,metadata=print:file=-", "-f", "null", "-")
}

// The pictures of a film: fitted inside 1920×1080, on black or on a dimmed,
// blurred copy of itself (the prototype's render).
const (
	fitted  = "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p"
	blurred = "split[back][front];[back]scale=480:270:force_original_aspect_ratio=increase,crop=480:270,gblur=sigma=12,scale=1920:1080,eq=brightness=-0.08[behind];" +
		"[front]scale=1920:1080:force_original_aspect_ratio=decrease[shown];[behind][shown]overlay=(W-w)/2:(H-h)/2,setsar=1,format=yuv420p"
)

func (t Tools) segmentCommand(r SegmentRequest) (string, []string) {
	slow := r.Slow
	if slow == 0 {
		slow = 1
	}
	picture := fitted
	if r.Blurred {
		picture = blurred
	}
	graph := turn(r.Turned) + picture
	if slow != 1 {
		graph += ",setpts=" + strconv.FormatFloat(slow, 'f', -1, 64) + "*PTS"
	}
	// tpad repeats the last frame when the source ends a frame or two early,
	// so the segment always has Frames frames. It must come before setpts:
	// after it, FFmpeg 7.1 ends the stream without padding.
	graph += ",fps=" + strconv.Itoa(FilmRate) + ",tpad=stop_mode=clone:stop=-1,setpts=PTS-STARTPTS,trim=end_frame=" + strconv.Itoa(r.Frames)
	if r.FadeFrames > 0 {
		graph += ",fade=t=out:start_frame=" + strconv.Itoa(r.Frames-r.FadeFrames) + ":nb_frames=" + strconv.Itoa(r.FadeFrames)
	}
	// Two frames more of source than needed: the frames land on 1/30 s.
	input := float64(r.Frames)/FilmRate/slow + 2.0/FilmRate
	offset := strconv.FormatFloat(float64(r.Offset)/FilmRate, 'f', 6, 64)
	args := append(quiet(), "-threads", editThreads)
	if r.Start > 0 {
		args = append(args, "-ss", seconds(r.Start))
	}
	args = append(args, "-t", strconv.FormatFloat(input, 'f', 6, 64), "-i", "fd:", "-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", graph, "-frames:v", strconv.Itoa(r.Frames),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-maxrate", "16M", "-bufsize", "16M", "-pix_fmt", "yuv420p",
		"-r", strconv.Itoa(FilmRate), "-threads", editThreads,
		// Without it the first segment, whose B-frames start before zero, is
		// moved two frames later than the others.
		"-avoid_negative_ts", "disabled", "-output_ts_offset", offset, "-f", "mpegts", "pipe:1")
	return t.FFmpeg, args
}

// assembleCommand reads the segments on standard input and the song on
// descriptor 3, and writes the film to descriptor 4 by its path: moving the
// index to the front reopens the output.
func (t Tools) assembleCommand(r AssembleRequest, created time.Time) (string, []string) {
	length := float64(r.Frames) / FilmRate
	fade := "afade=t=in:d=" + strconv.FormatFloat(filmFadeIn, 'f', -1, 64) +
		",afade=t=out:st=" + seconds(max(0, length-filmFadeOut)) + ":d=" + strconv.FormatFloat(filmFadeOut, 'f', -1, 64)
	args := append(quiet(), "-y", "-i", "fd:")
	if r.SongStart > 0 {
		args = append(args, "-ss", seconds(r.SongStart))
	}
	args = append(args, "-t", seconds(length), "-fd", "3", "-i", "fd:",
		"-map", "0:v:0", "-map", "1:a:0", "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", "-ac", "2", "-ar", "48000",
		"-af", fade, "-t", seconds(length), "-map_metadata", "-1", "-map_chapters", "-1",
		"-metadata", "creation_time="+created.UTC().Format("2006-01-02T15:04:05.000000Z"),
		"-movflags", "+faststart", "-fs", strconv.Itoa(maxFilmSize), "-f", "mp4", "/proc/self/fd/4")
	return t.FFmpeg, args
}

// background starts a command below playback's priority.
func background(command *exec.Cmd) error {
	if err := command.Start(); err != nil {
		return err
	}
	_ = unix.Setpriority(unix.PRIO_PROCESS, command.Process.Pid, backgroundNice)
	return nil
}

func runBackground(command *exec.Cmd) error {
	if err := background(command); err != nil {
		return err
	}
	return command.Wait()
}

func newCommand(ctx context.Context, program string, args []string) (*exec.Cmd, *limitedBuffer) {
	command := exec.CommandContext(ctx, program, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C.UTF-8"}
	stderr := &limitedBuffer{limit: 8 << 10}
	command.Stderr = stderr
	return command, stderr
}

// serveEditing answers an editing request. files are the descriptors that
// came with it, already at their start.
func serveEditing(ctx context.Context, conn *net.UnixConn, tools Tools, op string, body []byte, files []*os.File) error {
	invalid := func(err error) error { return writeFrame(conn, Response{Code: CodeInvalid, Message: err.Error()}, nil) }
	want := map[string]int{OpFrames: 1, OpSegment: 1, OpAssemble: 2}[op]
	if len(files) != want {
		return invalid(fmt.Errorf("%s takes %d file descriptors", op, want))
	}
	if tools.FFmpeg == "" {
		return writeFrame(conn, Response{Code: CodeUnavailable, Message: "decoder is unavailable"}, nil)
	}
	// The peer never writes after its request: a read that returns means it
	// has gone, and the work stops.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _, _ = conn.Read(make([]byte, 1)); cancel() }()
	switch op {
	case OpFrames:
		var request framesWire
		if err := decodeStrict(body, &request); err != nil {
			return invalid(err)
		}
		count, err := request.count()
		if err != nil {
			return invalid(err)
		}
		return serveFrames(ctx, conn, tools, request.FramesRequest, count, files[0])
	case OpSegment:
		var request segmentWire
		if err := decodeStrict(body, &request); err != nil {
			return invalid(err)
		}
		if err := request.validate(); err != nil {
			return invalid(err)
		}
		ctx, stop := context.WithTimeout(ctx, segmentTimeout)
		defer stop()
		program, args := tools.segmentCommand(request.SegmentRequest)
		command, stderr := newCommand(ctx, program, args)
		command.Stdin = files[0]
		stdout := &limitedBuffer{limit: maxSegmentSize}
		command.Stdout = stdout
		err := runBackground(command)
		if err != nil || stdout.overflow || stdout.Len() == 0 {
			return writeFrame(conn, editFailure(err, stderr, stdout.overflow), nil)
		}
		if err := writeFrame(conn, Response{OK: true, Size: int64(stdout.Len())}, nil); err != nil {
			return err
		}
		_, err = conn.Write(stdout.Bytes())
		return err
	default:
		var request assembleWire
		if err := decodeStrict(body, &request); err != nil {
			return invalid(err)
		}
		created, err := request.validate()
		if err != nil {
			return invalid(err)
		}
		return serveAssemble(ctx, conn, tools, request.AssembleRequest, created, files)
	}
}

// editFailure is the response for a failed editing command: output past its
// limit is too large, never unreadable.
func editFailure(err error, stderr *limitedBuffer, overflow bool) Response {
	if overflow {
		return Response{Code: CodeTooLarge, Message: "output is too large"}
	}
	return failure(err, stderr, false)
}

func serveAssemble(ctx context.Context, conn *net.UnixConn, tools Tools, request AssembleRequest, created time.Time, files []*os.File) error {
	fd, err := unix.MemfdCreate("anas-film", unix.MFD_CLOEXEC)
	if err != nil {
		return writeFrame(conn, Response{Code: CodeFailed, Message: err.Error()}, nil)
	}
	film := os.NewFile(uintptr(fd), "anas-film")
	defer func() { _ = film.Close() }()
	ctx, stop := context.WithTimeout(ctx, filmTimeout)
	defer stop()
	program, args := tools.assembleCommand(request, created)
	command, stderr := newCommand(ctx, program, args)
	command.Stdin = files[0]
	command.ExtraFiles = []*os.File{files[1], film}
	if err := runBackground(command); err != nil {
		return writeFrame(conn, editFailure(err, stderr, false), nil)
	}
	size, err := film.Seek(0, io.SeekEnd)
	switch {
	case err != nil:
		return writeFrame(conn, Response{Code: CodeFailed, Message: err.Error()}, nil)
	case size >= maxFilmSize:
		// -fs stops writing at the limit; the film would be cut short.
		return writeFrame(conn, Response{Code: CodeTooLarge, Message: "the film is too large"}, nil)
	case size == 0:
		return writeFrame(conn, Response{Code: CodeUnreadable, Message: "no output"}, nil)
	}
	if _, err := film.Seek(0, io.SeekStart); err != nil {
		return writeFrame(conn, Response{Code: CodeFailed, Message: err.Error()}, nil)
	}
	if err := writeFrame(conn, Response{OK: true, Size: size}, nil); err != nil {
		return err
	}
	_, err = io.Copy(conn, film)
	return err
}

// serveFrames answers at once, then sends each frame as a message, an empty
// message, and the trailer that says whether they all came. A frames
// request can fail after the first frames went out; the trailer says so,
// and the caller drops what it received.
func serveFrames(ctx context.Context, conn *net.UnixConn, tools Tools, request FramesRequest, count int, file *os.File) error {
	ctx, stop := context.WithTimeout(ctx, request.timeout())
	defer stop()
	if err := writeFrame(conn, Response{OK: true}, nil); err != nil {
		return err
	}
	sender := frameSender{conn: conn, tools: tools, request: request, most: count}
	var outcome Response
	if len(request.Times) > 0 {
		outcome = sender.atTimes(ctx, file)
	} else {
		outcome = sender.inRange(ctx, file)
	}
	if err := unixmsg.WriteBody(conn, nil, nil); err != nil {
		return err
	}
	trailer := framesTrailer{Response: outcome}
	if outcome.OK {
		trailer.FramesResult = sender.result
	}
	body, err := unixmsg.Encode(trailer, maxFrame)
	if err != nil {
		return err
	}
	return unixmsg.WriteBody(conn, body, nil)
}

type frameSender struct {
	conn    *net.UnixConn
	tools   Tools
	request FramesRequest
	most    int
	sent    int
	result  FramesResult
}

var errTooLarge = errors.New("output is too large")

// send measures a frame if asked and sends it.
func (s *frameSender) send(ctx context.Context, frame []byte) error {
	s.sent++
	if s.sent > s.most || len(frame) > maxFrameBytes {
		return errTooLarge
	}
	if s.request.Blur {
		blur, err := s.tools.blur(ctx, frame)
		if err != nil {
			return err
		}
		s.result.Blur = append(s.result.Blur, blur)
	}
	return unixmsg.WriteBody(s.conn, frame, nil)
}

// outcome turns how the frames ended into the trailer's response.
func outcome(ctx context.Context, err error, stderr *limitedBuffer) Response {
	if stderr == nil {
		stderr = &limitedBuffer{}
	}
	var exitError *exec.ExitError
	switch {
	case err == nil:
		return Response{OK: true}
	case errors.Is(err, errTooLarge):
		return Response{Code: CodeTooLarge, Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		return Response{Code: CodeFailed, Message: "timed out or cancelled"}
	case errors.As(err, &exitError), errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		return failure(err, stderr, false)
	case errors.Is(err, ErrUnreadable):
		return Response{Code: CodeUnreadable, Message: err.Error()}
	}
	return Response{Code: CodeFailed, Message: err.Error()}
}

// earlier are the steps back from an instant with no frame: the last frames
// of some cameras' files end before the file does.
var earlier = []float64{0, 0.5, 1, 2, 4}

func (s *frameSender) atTimes(ctx context.Context, file *os.File) Response {
	for _, at := range s.request.Times {
		frame, used, stderr, err := s.near(ctx, file, at)
		if err == nil && s.request.Format == FormatGray && len(frame) != s.request.Width*s.request.Height {
			err = fmt.Errorf("gray frame of %d bytes", len(frame))
		}
		if err == nil {
			err = s.send(ctx, frame)
		}
		if err != nil {
			return outcome(ctx, err, stderr)
		}
		s.result.Times = append(s.result.Times, used)
	}
	return Response{OK: true}
}

// near takes the frame at at, or the nearest earlier one FFmpeg finds. Past
// the last frame FFmpeg 7.1 fails ("Nothing was written into output file")
// rather than write nothing, so a failure also steps back; when every step
// fails, the first failure is the answer.
func (s *frameSender) near(ctx context.Context, file *os.File, at float64) ([]byte, float64, *limitedBuffer, error) {
	var failed error
	var failedStderr *limitedBuffer
	tried := -1.0
	for _, back := range earlier {
		used := max(0, at-back)
		if used == tried {
			break
		}
		tried = used
		frame, stderr, err := s.one(ctx, file, used)
		switch {
		case err == nil && len(frame) > 0:
			return frame, used, stderr, nil
		case ctx.Err() != nil, errors.Is(err, errTooLarge):
			return nil, used, stderr, err
		case err != nil && failed == nil:
			failed, failedStderr = err, stderr
		}
	}
	if failed == nil {
		failed = fmt.Errorf("%w: no frame at %.3f s", ErrUnreadable, at)
	}
	return nil, at, failedStderr, failed
}

// one runs FFmpeg for the frame at at, from the start of the file.
func (s *frameSender) one(ctx context.Context, file *os.File, at float64) ([]byte, *limitedBuffer, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("%w: the file cannot seek", ErrUnreadable)
	}
	program, args := s.tools.frameCommand(s.request, at)
	command, stderr := newCommand(ctx, program, args)
	command.Stdin = file
	stdout := &limitedBuffer{limit: maxFrameBytes}
	command.Stdout = stdout
	if err := runBackground(command); err != nil {
		return nil, stderr, err
	}
	if stdout.overflow {
		return nil, stderr, errTooLarge
	}
	return stdout.Bytes(), stderr, nil
}

func (s *frameSender) inRange(ctx context.Context, file *os.File) Response {
	program, args := s.tools.rangeCommand(s.request)
	command, stderr := newCommand(ctx, program, args)
	command.Stdin = file
	stdout, err := command.StdoutPipe()
	if err != nil {
		return Response{Code: CodeFailed, Message: err.Error()}
	}
	if err := background(command); err != nil {
		return outcome(ctx, err, stderr)
	}
	if s.request.Format == FormatGray {
		err = s.grayFrames(ctx, stdout)
	} else {
		err = s.jpegFrames(ctx, stdout)
	}
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return outcome(ctx, err, stderr)
	}
	return outcome(ctx, command.Wait(), stderr)
}

func (s *frameSender) grayFrames(ctx context.Context, stdout io.Reader) error {
	size := s.request.Width * s.request.Height
	for {
		frame := make([]byte, size)
		_, err := io.ReadFull(stdout, frame)
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, io.ErrUnexpectedEOF):
			return errors.New("the last gray frame is incomplete")
		case err != nil:
			return err
		}
		if err := s.send(ctx, frame); err != nil {
			return err
		}
	}
}

// jpegFrames splits FFmpeg's multipart JPEG stream.
func (s *frameSender) jpegFrames(ctx context.Context, stdout io.Reader) error {
	parts := multipart.NewReader(stdout, boundary)
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("frames stream: %w", err)
		}
		frame, err := io.ReadAll(io.LimitReader(part, maxFrameBytes+1))
		if err != nil {
			return fmt.Errorf("frames stream: %w", err)
		}
		if err := s.send(ctx, frame); err != nil {
			return err
		}
	}
}

// blur runs blurdetect on one encoded JPEG. Its value for a flat picture is
// nan, which comes back as nil.
func (t Tools) blur(ctx context.Context, frame []byte) (*float64, error) {
	ctx, stop := context.WithTimeout(ctx, blurTimeout)
	defer stop()
	program, args := t.blurCommand()
	command, _ := newCommand(ctx, program, args)
	command.Stdin = bytes.NewReader(frame)
	stdout := &limitedBuffer{limit: 64 << 10}
	command.Stdout = stdout
	if err := runBackground(command); err != nil {
		return nil, fmt.Errorf("%w: blurdetect: %v", ErrUnreadable, err)
	}
	lines := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
	for lines.Scan() {
		if value, ok := strings.CutPrefix(strings.TrimSpace(lines.Text()), "lavfi.blur="); ok {
			blur, err := strconv.ParseFloat(value, 64)
			switch {
			case err != nil:
			case math.IsNaN(blur) || math.IsInf(blur, 0):
				return nil, nil
			default:
				return &blur, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: blurdetect gave no value", ErrUnreadable)
}
