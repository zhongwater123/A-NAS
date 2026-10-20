package photos

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// A photo's view is what a member's AI assistant hands its model when the
// member asks it to look at the photo (ADR 0024 decision 6). Hermes and the
// model providers scale larger images down to about this long edge anyway.
const (
	viewEdge    = 1600
	viewQuality = 85
)

// ErrNoView reports a video, which has no view.
var ErrNoView = errors.New("only photos have a view")

// View renders a visible photo for a model to look at: an upright JPEG at
// most viewEdge pixels on its long edge, encoded afresh from the pixels so
// that none of the original's metadata, its location among it, goes along.
// Members' assistants get this instead of the original or the thumbnail;
// videos have no view.
func (s *Service) View(ctx context.Context, p Principal, assetID string) (Content, error) {
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return Content{}, err
	}
	if !strings.HasPrefix(record.MediaType, "image/") {
		return Content{}, ErrNoView
	}
	original, err := s.openObject(record.objectID)
	if errors.Is(err, fs.ErrNotExist) {
		return Content{}, fmt.Errorf("original of %s is missing from the content store", record.ID)
	}
	if err != nil {
		return Content{}, err
	}
	defer original.Close()
	data, _, _, err := s.renderPhoto(ctx, original, record.orientation, viewEdge, viewQuality)
	if err != nil {
		return Content{}, err
	}
	file, err := memoryFile(data)
	if err != nil {
		return Content{}, err
	}
	return Content{
		Name: strings.TrimSuffix(record.Name, filepath.Ext(record.Name)) + ".jpg", MediaType: "image/jpeg",
		SizeBytes: int64(len(data)), ETag: `"` + record.objectID + `-view-v1"`, Reader: file,
	}, nil
}
