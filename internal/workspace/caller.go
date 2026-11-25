package workspace

import "context"

// Caller is who a request to the workspace comes from: the account, as the
// Host Agent confirmed it, and whether it is the desktop or one of the
// account's agents.
type Caller struct {
	Actor Actor
	Via   Via
	// Origin is the scheme and host an agent reached the NAS at, for the
	// links it is given; empty for the desktop.
	Origin string
}

// Agent reports whether the caller is a member's agent.
func (c Caller) Agent() bool { return c.Via.TokenID != "" }

type callerKey struct{}

// WithCaller attaches the confirmed caller to ctx.
func WithCaller(ctx context.Context, caller Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// CallerFrom returns the caller WithCaller attached.
func CallerFrom(ctx context.Context) (Caller, bool) {
	caller, ok := ctx.Value(callerKey{}).(Caller)
	return caller, ok && caller.Actor.AccountID != ""
}
