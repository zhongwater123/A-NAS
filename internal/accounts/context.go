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
