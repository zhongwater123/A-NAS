package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
)

// WorkerProcessor is the media center's Processor on the shared FFmpeg
// worker. The worker only runs typed operations; what a probe means for a
// film or an episode is decided here.
type WorkerProcessor struct {
	Worker ffmpegworker.Worker
}

var _ Processor = WorkerProcessor{}

func (p WorkerProcessor) Probe(ctx context.Context, file *os.File) (MediaInfo, error) {
	probe, err := p.Worker.Probe(ctx, file)
	if err != nil {
		return MediaInfo{}, workerError(err)
	}
	return mediaInfo(probe), nil
}

func (p WorkerProcessor) Frame(ctx context.Context, file *os.File, at float64, width int) ([]byte, error) {
	frame, err := p.Worker.Frame(ctx, file, at, width)
	return frame, workerError(err)
}

func (p WorkerProcessor) Subtitle(ctx context.Context, file *os.File, source SubtitleSource) ([]byte, error) {
	subtitle, err := p.Worker.Subtitle(ctx, file, ffmpegworker.SubtitleRequest{Format: source.Format, Charset: source.Charset, Stream: source.Stream})
	return subtitle, workerError(err)
}

func (p WorkerProcessor) Stream(ctx context.Context, file *os.File, options StreamOptions) (io.ReadCloser, error) {
	stream, err := p.Worker.Stream(ctx, file, ffmpegworker.StreamRequest{
		Start: options.Start, CopyVideo: options.CopyVideo, HEVC: options.HEVC, Height: options.Height,
		MaxBitrate: options.MaxBitrate, AudioTrack: options.AudioTrack, CopyAudio: options.CopyAudio,
	})
	return stream, workerError(err)
}

// workerError keeps the media center's errors for the worker's.
func workerError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ffmpegworker.ErrUnavailable):
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	case errors.Is(err, ffmpegworker.ErrUnreadable):
		return fmt.Errorf("%w: %w", ErrUnreadable, err)
	case errors.Is(err, ffmpegworker.ErrInvalid):
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return err
}

// mediaInfo keeps the first real video stream (not cover art), every audio
// stream and every subtitle stream.
func mediaInfo(probe ffmpegworker.Probe) MediaInfo {
	info := MediaInfo{Container: probe.Format.Name, Duration: probe.Format.Duration, Bitrate: probe.Format.Bitrate}
	for _, stream := range probe.Streams {
		language := stream.Tags["language"]
		if language == "und" {
			language = ""
		}
		switch stream.Type {
		case "video":
			if info.Video != nil || stream.AttachedPic || stream.Codec == "mjpeg" || stream.Codec == "png" {
				continue
			}
			video := &VideoStream{
				Index: stream.Index, Codec: stream.Codec, Profile: stream.Profile, Width: stream.Width, Height: stream.Height,
				PixelFormat: stream.PixelFormat, FrameRate: stream.FrameRate,
			}
			switch stream.ColorTransfer {
			case "smpte2084":
				video.HDR = "HDR10"
			case "arib-std-b67":
				video.HDR = "HLG"
			}
			for _, side := range stream.SideData {
				if strings.Contains(side, "DOVI") || strings.Contains(side, "Dolby Vision") {
					video.HDR = "Dolby Vision"
				}
			}
			info.Video = video
		case "audio":
			info.Audio = append(info.Audio, AudioStream{
				Index: stream.Index, Codec: stream.Codec, Channels: stream.Channels, Language: language,
				Title: stream.Tags["title"], Default: stream.Default,
			})
		case "subtitle":
			info.Subtitles = append(info.Subtitles, SubtitleStream{
				Index: stream.Index, Codec: stream.Codec, Language: language, Title: stream.Tags["title"],
				Default: stream.Default, Forced: stream.Forced,
			})
		}
	}
	return info
}
