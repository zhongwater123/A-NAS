package vectorindex

import "time"

// SetIdleTimeout shortens how long an unused connection stays open.
func SetIdleTimeout(d time.Duration) (restore func()) {
	previous := idleTimeout
	idleTimeout = d
	return func() { idleTimeout = previous }
}
