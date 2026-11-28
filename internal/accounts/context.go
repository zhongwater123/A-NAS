package accounts

import "context"

type userKey struct{}

// WithUser attaches the signed-in user. The Product Service passes it this
// way to the handlers it mounts under a prefix, and the sharing service to
// its API; they read it with UserFrom (#222).
func WithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// UserFrom returns the user WithUser attached; a user without an ID is none.
func UserFrom(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userKey{}).(User)
	return user, ok && user.ID != ""
}

type deviceTokenKey struct{}

// WithDeviceToken attaches the e-ink screen's device token (ADR 0041), which
// the Product Service's guard resolved from the bearer token. The handlers
// read it back with DeviceTokenFrom; the token is family-level, so there is
// no user.
func WithDeviceToken(ctx context.Context, token DeviceToken) context.Context {
	return context.WithValue(ctx, deviceTokenKey{}, token)
}

// DeviceTokenFrom returns the device token WithDeviceToken attached.
func DeviceTokenFrom(ctx context.Context) (DeviceToken, bool) {
	token, ok := ctx.Value(deviceTokenKey{}).(DeviceToken)
	return token, ok && token.ID != ""
}
