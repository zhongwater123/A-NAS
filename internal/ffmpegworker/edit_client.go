package ffmpegworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// Editing is what editing a film needs from the worker; Client and Local
// provide it. Each call takes a background slot unless ctx says otherwise.
type Editing interface {
	// Frames calls each with every frame as it arrives, then returns what
	// the worker measured. When it returns an error, the frames each saw
	// are incomplete and must be dropped; an error from each stops it.
	Frames(ctx context.Context, file *os.File, request FramesRequest, each func(frame []byte) error) (FramesResult, error)
	// Segment returns a piece of the film as MPEG-TS. Reading it to the
	// end without an error means all of it came.
	Segment(ctx context.Context, file *os.File, request SegmentRequest) (io.ReadCloser, error)
	// Assemble returns the film as MP4, from the segments written one after
	// another into video and the song.
	Assemble(ctx context.Context, video, song *os.File, request AssembleRequest) (io.ReadCloser, error)
}

func frames(ctx context.Context, dial dialer, file *os.File, request FramesRequest, each func([]byte) error) (FramesResult, error) {
	response, _, err := send(ctx, dial, OpFrames, framesWire{Op: OpFrames, Ack: true, FramesRequest: request}, []*os.File{file})
	if err != nil {
		return FramesResult{}, err
	}
	defer func() { _ = response.Close() }()
	lost := func(err error) error {
		if response.ctx.Err() != nil {
			return response.ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrInterrupted, err)
	}
	for {
		frame, extra, err := unixmsg.ReadBody(response.UnixConn, maxFrameBytes)
		unixmsg.CloseFile(extra)
		if err != nil {
			return FramesResult{}, lost(err)
		}
		if len(frame) == 0 {
			break
		}
		if err := each(frame); err != nil {
			return FramesResult{}, err
		}
	}
	data, extra, err := unixmsg.ReadBody(response.UnixConn, maxFrame)
	unixmsg.CloseFile(extra)
	if err != nil {
		return FramesResult{}, lost(err)
	}
	var trailer framesTrailer
	if err := json.Unmarshal(data, &trailer); err != nil {
		return FramesResult{}, fmt.Errorf("%w: frames trailer: %v", ErrInterrupted, err)
	}
	if !trailer.OK {
		return FramesResult{}, responseError(trailer.Response)
	}
	return trailer.FramesResult, nil
}

// sized is a body whose length the response announced: one that ends early
// reports the worker lost.
type sized struct {
	body
	left int64
}

func (s *sized) Read(p []byte) (int, error) {
	if s.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, err := s.body.Read(p)
	s.left -= int64(n)
	if errors.Is(err, io.EOF) && s.left > 0 {
		return n, fmt.Errorf("%w: the output ended %d bytes early", ErrInterrupted, s.left)
	}
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return n, err
}

func sizedCall(ctx context.Context, dial dialer, op string, message any, files []*os.File) (io.ReadCloser, error) {
	response, header, err := send(ctx, dial, op, message, files)
	if err != nil {
		return nil, err
	}
	return &sized{body: response, left: header.Size}, nil
}

func segment(ctx context.Context, dial dialer, file *os.File, request SegmentRequest) (io.ReadCloser, error) {
	return sizedCall(ctx, dial, OpSegment, segmentWire{Op: OpSegment, Ack: true, SegmentRequest: request}, []*os.File{file})
}

func assemble(ctx context.Context, dial dialer, video, song *os.File, request AssembleRequest) (io.ReadCloser, error) {
	return sizedCall(ctx, dial, OpAssemble, assembleWire{Op: OpAssemble, Ack: true, AssembleRequest: request}, []*os.File{video, song})
}

func (c Client) Frames(ctx context.Context, file *os.File, request FramesRequest, each func([]byte) error) (FramesResult, error) {
	return frames(ctx, c.dial, file, request, each)
}
func (c Client) Segment(ctx context.Context, file *os.File, request SegmentRequest) (io.ReadCloser, error) {
	return segment(ctx, c.dial, file, request)
}
func (c Client) Assemble(ctx context.Context, video, song *os.File, request AssembleRequest) (io.ReadCloser, error) {
	return assemble(ctx, c.dial, video, song, request)
}

func (l Local) Frames(ctx context.Context, file *os.File, request FramesRequest, each func([]byte) error) (FramesResult, error) {
	return frames(ctx, l.dial, file, request, each)
}
func (l Local) Segment(ctx context.Context, file *os.File, request SegmentRequest) (io.ReadCloser, error) {
	return segment(ctx, l.dial, file, request)
}
func (l Local) Assemble(ctx context.Context, video, song *os.File, request AssembleRequest) (io.ReadCloser, error) {
	return assemble(ctx, l.dial, video, song, request)
}
