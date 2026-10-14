package photos

import "time"

// SetPressureRecheck shortens how soon RunAI looks again after a pressure
// pause, for tests; the returned function restores it.
func SetPressureRecheck(d time.Duration) (restore func()) {
	previous := pressureRecheck
	pressureRecheck = d
	return func() { pressureRecheck = previous }
}
