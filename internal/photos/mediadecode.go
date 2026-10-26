package photos

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// MediaDecoder accepts only already-authorized read-only descriptors. Production
// decoding remains inside the isolated Media Worker, never the photo service.
type MediaDecoder interface {
	Probe(context.Context, *os.File) (mediaworker.Probe, error)
	Image(context.Context, *os.File, int) ([]byte, error)
	// Frame renders the video frame at a time, upright, at most width wide.
	Frame(context.Context, *os.File, float64, int) ([]byte, error)
	Stream(context.Context, *os.File, mediaworker.StreamRequest) (io.ReadCloser, error)
}

func readHEICMetadata(file *os.File, location *time.Location) (metadata photoMetadata, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("photo HEIC metadata", recovered)
			metadata = photoMetadata{}
			err = ErrUnsupportedType
		}
	}()
	metadata.width, metadata.height, err = heifDimensions(file)
	if err != nil {
		return metadata, err
	}
	_, _ = file.Seek(0, io.SeekStart)
	_, metadata.takenAt = readEXIF(file, location)
	// libheif applies the HEIF container's display transforms itself.
	metadata.orientation = 1
	return metadata, nil
}

// heicTimeout bounds decoding one HEIC once the Media Worker has taken it;
// waiting for a worker does not count.
const heicTimeout = 30 * time.Second

func (s *Service) renderPhoto(ctx context.Context, file *os.File, orientation, edge, quality int) ([]byte, int, int, error) {
	var header [32]byte
	n, _ := file.ReadAt(header[:], 0)
	if sniffMediaType(header[:n]) != "image/heic" {
		return renderUpright(ctx, file, orientation, edge, quality)
	}
	if s.media == nil {
		return nil, 0, 0, mediaworker.ErrUnavailable
	}
	data, err := s.media.Image(mediaworker.WithTimeout(ctx, heicTimeout), file, 1600)
	if err != nil {
		return nil, 0, 0, err
	}
	return renderUpright(ctx, bytes.NewReader(data), 1, edge, quality)
}

// Preview returns a compatible JPEG for HEIC and a video's poster frame;
// originals stay immutable.
func (s *Service) Preview(ctx context.Context, p Principal, id string) (Content, error) {
	record, err := s.visibleAsset(ctx, s.db, p, id)
	if err != nil {
		return Content{}, err
	}
	original, err := s.Open(ctx, p, id)
	if err != nil {
		return Content{}, err
	}
	if record.Video != nil {
		defer func() { _ = original.Reader.Close() }()
		return s.poster(ctx, record, original.Reader)
	}
	if record.MediaType != "image/heic" {
		return original, nil
	}
	defer func() { _ = original.Reader.Close() }()
	data, _, _, err := s.renderPhoto(ctx, original.Reader, 1, 1600, 92)
	if err != nil {
		return Content{}, err
	}
	file, err := memoryFile(data)
	if err != nil {
		return Content{}, err
	}
	return Content{Name: record.Name + ".jpg", MediaType: "image/jpeg", SizeBytes: int64(len(data)), ETag: `"` + record.objectID + `-preview-v1"`, Reader: file}, nil
}
