package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
)

func TestProbeBecomesMediaInfo(t *testing.T) {
	probe, err := ffmpegworker.ParseProbe([]byte(`{
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
	info := mediaInfo(probe)
	if info.Container != "matroska,webm" || info.Duration != 7212.48 || info.Video == nil || info.Video.Codec != "hevc" ||
		info.Video.HDR != "Dolby Vision" || info.Video.FrameRate < 23.97 || info.Video.FrameRate > 23.98 {
		t.Fatalf("info = %+v / %+v", info, info.Video)
	}
	if len(info.Audio) != 2 || info.Audio[0].Title != "国语" || !info.Audio[0].Default || info.Audio[1].Language != "" || len(info.Subtitles) != 2 {
		t.Fatalf("streams = %+v %+v", info.Audio, info.Subtitles)
	}
}

type failingWorker struct {
	ffmpegworker.Worker
	err error
}

func (w failingWorker) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{}, w.err
}
func (w failingWorker) Stream(context.Context, *os.File, ffmpegworker.StreamRequest) (io.ReadCloser, error) {
	return nil, w.err
}

func TestWorkerErrorsBecomeMediaCenterErrors(t *testing.T) {
	cases := map[error]error{
		ffmpegworker.ErrUnavailable: ErrUnavailable,
		ffmpegworker.ErrUnreadable:  ErrUnreadable,
		ffmpegworker.ErrInvalid:     ErrInvalid,
	}
	for workerErr, want := range cases {
		processor := WorkerProcessor{Worker: failingWorker{err: fmt.Errorf("%w: detail", workerErr)}}
		if _, err := processor.Probe(context.Background(), nil); !errors.Is(err, want) || !errors.Is(err, workerErr) {
			t.Errorf("Probe() error = %v, want %v", err, want)
		}
		if _, err := processor.Stream(context.Background(), nil, StreamOptions{}); !errors.Is(err, want) {
			t.Errorf("Stream() error = %v, want %v", err, want)
		}
	}
}
