package accountid

import (
	"errors"
	"strings"
	"time"
)

// Device tokens are the family-level credentials the e-ink screen (M1) holds
// (ADR 0041). The Product Service issues them into control.db; it is also the
// final handler of the three device routes, so it resolves them itself. A
// device token is not bound to an account: the screen belongs to the whole
// family and must survive any one account's disable, reset or deletion.
const (
	// DeviceTokenPrefix starts every device token, so secret scanners and
	// people can tell one from a session or agent token.
	DeviceTokenPrefix = "anas_dev_"
	// DeviceTokenIdleLimit ends a token nobody used for this long.
	DeviceTokenIdleLimit = 90 * 24 * time.Hour
)

// ErrDeviceTokenNotFound reports a token that is unknown, revoked, or idle
// past DeviceTokenIdleLimit.
var ErrDeviceTokenNotFound = errors.New("device token is invalid or revoked")

// LooksLikeDeviceToken reports whether value has a device token's shape; it
// saves a lookup for what cannot be one.
func LooksLikeDeviceToken(value string) bool {
	rest, ok := strings.CutPrefix(value, DeviceTokenPrefix)
	if !ok || len(rest) != 64 {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// DeviceTokenIdle reports whether a token last used (or created, if never
// used) at lastUse has gone unused past DeviceTokenIdleLimit at now.
func DeviceTokenIdle(lastUse, now time.Time) bool {
	return !now.Before(lastUse.Add(DeviceTokenIdleLimit))
}
