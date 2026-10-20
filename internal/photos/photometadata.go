package photos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"strings"

	"github.com/evanoberholster/imagemeta"
)

// Metadata is what a photo's original says about itself, read when asked:
// everything its EXIF records (the main image tags, the Exif tags such as
// camera, lens and exposure, and GPS) and, when the camera recorded one,
// where the photo was taken. Members and their AI assistants read it for the
// photos they can see (ADR 0024 decision 6).
type Metadata struct {
	Location *Location `json:"location,omitempty"`
	// EXIF holds the parsed tags under ifd0, exif and gps; absent when the
	// original carries none.
	EXIF json.RawMessage `json:"exif,omitempty"`
}

// Location is a photo's GPS position in degrees; Altitude is in metres above
// sea level when the camera recorded it.
type Location struct {
	Latitude  float64  `json:"latitude"`
	Longitude float64  `json:"longitude"`
	Altitude  *float64 `json:"altitude,omitempty"`
}

// ErrNoMetadata reports a video, whose recording details are not read yet.
var ErrNoMetadata = errors.New("only photos have metadata here")

// Metadata reads a visible photo's metadata from its original.
func (s *Service) Metadata(ctx context.Context, p Principal, assetID string) (Metadata, error) {
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return Metadata{}, err
	}
	if !strings.HasPrefix(record.MediaType, "image/") {
		return Metadata{}, ErrNoMetadata
	}
	original, err := s.openObject(record.objectID)
	if errors.Is(err, fs.ErrNotExist) {
		return Metadata{}, fmt.Errorf("original of %s is missing from the content store", record.ID)
	}
	if err != nil {
		return Metadata{}, err
	}
	defer original.Close()
	return decodeMetadata(original), nil
}

// decodeMetadata never fails: an original without EXIF, or with EXIF the
// parser cannot read (it may panic on hostile input), has no metadata.
func decodeMetadata(r io.ReadSeeker) (metadata Metadata) {
	defer func() {
		if recover() != nil {
			metadata = Metadata{}
		}
	}()
	parsed, err := imagemeta.Decode(r)
	if err != nil {
		return Metadata{}
	}
	tags, err := json.Marshal(struct {
		IFD0 any `json:"ifd0"`
		Exif any `json:"exif"`
		GPS  any `json:"gps"`
	}{parsed.IFD0, parsed.ExifIFD, parsed.GPS})
	if err == nil {
		metadata.EXIF = tags
	}
	latitude, longitude := parsed.GPS.Latitude(), parsed.GPS.Longitude()
	// Both zero is a camera that wrote GPS tags without a fix.
	if (latitude != 0 || longitude != 0) && math.Abs(latitude) <= 90 && math.Abs(longitude) <= 180 {
		metadata.Location = &Location{Latitude: latitude, Longitude: longitude}
		if altitude := float64(parsed.GPS.Altitude()); altitude != 0 {
			metadata.Location.Altitude = &altitude
		}
	}
	return metadata
}
