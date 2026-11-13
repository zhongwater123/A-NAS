package photos

import (
	"io"
	"time"

	"github.com/zhongwater123/A-NAS/internal/imagerender"
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
	return imagerender.ReadEXIF(r, location, "photo EXIF")
}
