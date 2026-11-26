package vectorindex

import "time"

// SetCallTimeout shortens how long a call may take.
func SetCallTimeout(d time.Duration) (restore func()) {
	previous := callTimeout
	callTimeout = d
	return func() { callTimeout = previous }
}
