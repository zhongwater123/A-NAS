package photos

import (
	"context"
	"database/sql"
	"time"
)

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

// QueueWindows queues object for the window index, as its triggers do.
func (s *Service) QueueWindows(ctx context.Context, object string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO window_outbox(object_id) VALUES (?)", object)
	return err
}

// QueueDescribe queues the card descriptions of object as the quality step
// of #556 will once it is done, for tests that stand in for it.
func (s *Service) QueueDescribe(ctx context.Context, object string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error { return s.queueDescribe(ctx, tx, object) })
}
