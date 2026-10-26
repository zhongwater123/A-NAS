package photos

import "time"

// AppleTIFFID and JPEGAPP1 expose the hand-written Exif readers to their fuzz
// test; imagemeta drops Apple's ContentIdentifier (see appleContentID).
var (
	AppleTIFFID = appleTIFFID
	JPEGAPP1    = jpegAPP1
)

// SetPressureRecheck shortens how soon RunAI looks again after a pressure
// pause, for tests; the returned function restores it.
func SetPressureRecheck(d time.Duration) (restore func()) {
	previous := pressureRecheck
	pressureRecheck = d
	return func() { pressureRecheck = previous }
}
