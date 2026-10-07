package photos

import (
	"io"
	"time"

	"github.com/evanoberholster/imagemeta"
)

// photoMetadata is what A-NAS reads from an original's header at import.
type photoMetadata struct {
	width, height int
	// orientation is the EXIF orientation, 1 (upright) to 8.
	orientation int
	takenAt     time.Time
}

// displaySize returns the dimensions after applying the EXIF orientation.
func (m photoMetadata) displaySize() (int, int) {
	if m.orientation >= 5 && m.orientation <= 8 {
		return m.height, m.width
	}
	return m.width, m.height
}

// readEXIF returns the orientation and capture time of an original. Missing
// or malformed EXIF yields orientation 1 and a zero time and never fails an
// import, including when the parser panics on hostile input. A capture time
// recorded without an offset is the camera's wall clock and is read in
// location, the NAS's local time zone.
func readEXIF(r io.ReadSeeker, location *time.Location) (orientation int, takenAt time.Time) {
	orientation = 1
	defer func() {
		if recover() != nil {
			orientation, takenAt = 1, time.Time{}
		}
	}()
	parsed, err := imagemeta.Decode(r)
	if err != nil {
		return 1, time.Time{}
	}
	if value := int(parsed.IFD0.Orientation); value >= 1 && value <= 8 {
		orientation = value
	}
	takenAt = parsed.ExifIFD.GetDateTimeOriginal()
	if takenAt.IsZero() || takenAt.Year() < 1900 {
		return orientation, time.Time{}
	}
	if parsed.ExifIFD.OffsetTimeOriginal == nil {
		takenAt = time.Date(takenAt.Year(), takenAt.Month(), takenAt.Day(),
			takenAt.Hour(), takenAt.Minute(), takenAt.Second(), takenAt.Nanosecond(), location)
	}
	return orientation, takenAt
}
