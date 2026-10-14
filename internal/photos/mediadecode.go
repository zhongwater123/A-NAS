package photos

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// MediaDecoder accepts only already-authorized read-only descriptors. Production
// decoding remains inside the isolated Media Worker, never the photo service.
type MediaDecoder interface {
	Probe(context.Context, *os.File) (mediaworker.Probe, error)
	Image(context.Context, *os.File, int) ([]byte, error)
	Stream(context.Context, *os.File, mediaworker.StreamRequest) (io.ReadCloser, error)
}

func readHEICMetadata(file *os.File, location *time.Location) (metadata photoMetadata, err error) {
	defer func() {
		if recover() != nil {
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

func (s *Service) renderPhoto(ctx context.Context, file *os.File, orientation, edge, quality int) ([]byte, int, int, error) {
	var header [32]byte
	n, _ := file.ReadAt(header[:], 0)
	if sniffMediaType(header[:n]) != "image/heic" {
		return renderUpright(file, orientation, edge, quality)
	}
	if s.media == nil {
		return nil, 0, 0, mediaworker.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case s.decodeSlots <- struct{}{}:
		defer func() { <-s.decodeSlots }()
	case <-ctx.Done():
		return nil, 0, 0, ctx.Err()
	}
	data, err := s.media.Image(ctx, file, 1600)
	if err != nil {
		return nil, 0, 0, err
	}
	return renderUpright(bytes.NewReader(data), 1, edge, quality)
}

// Preview returns a compatible JPEG for HEIC; originals stay immutable.
func (s *Service) Preview(ctx context.Context, p Principal, id string) (Content, error) {
	record, err := s.visibleAsset(ctx, s.db, p, id)
	if err != nil {
		return Content{}, err
	}
	original, err := s.Open(ctx, p, id)
	if err != nil {
		return Content{}, err
	}
	if record.MediaType != "image/heic" {
		return original, nil
	}
	defer original.Reader.Close()
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
