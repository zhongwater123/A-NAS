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

// CheckRunning reports whether RunVectorCheck is taking searches.
func (s *Service) CheckRunning() bool { return s.checking.Load() }

// SetIndexOverfetch sets how many more objects than it needs ranking first
// asks the index for, and returns a function that restores it.
func SetIndexOverfetch(n int) func() {
	previous := indexOverfetch
	indexOverfetch = n
	return func() { indexOverfetch = previous }
}

// UseVectorIndex makes ranking read index, or the Catalog alone when it is
// nil, as Options.Vectors would.
func UseVectorIndex(s *Service, index VectorIndex) { s.vectors = index }

// VectorSyncBackoff is how long vector sync waits after failures in a row.
func VectorSyncBackoff(idle time.Duration, failures int) time.Duration {
	return vectorSyncBackoff(idle, failures)
}
