package vectorindex

import "time"

// SetCallTimeout shortens how long a call may take.
func SetCallTimeout(d time.Duration) (restore func()) {
	previous := callTimeout
	callTimeout = d
	return func() { callTimeout = previous }
}

// SetIdleTimeout shortens how long an unused connection stays open.
func SetIdleTimeout(d time.Duration) (restore func()) {
	previous := idleTimeout
	idleTimeout = d
	return func() { idleTimeout = previous }
}
