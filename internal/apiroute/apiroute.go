// Package apiroute declares the product API's routes as data. Each route
// names who may call it, its posture, and internal/httpapi checks that
// before the handler runs, so a handler never decides on its own whether a
// session, the CSRF token or an administrator is needed (#192).
//
// A domain's HTTP package (internal/<domain>api) returns its routes from a
// Routes method; the Product Service puts them into one table with its own.
package apiroute

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// Posture is who may call a route.
type Posture int

const (
	// Public routes need no session: the health check, setting up the
	// device, signing in, and what the local screen's sign-in page reads.
	Public Posture = iota
	// Session routes need a signed-in user. A route with a method only
	// reads; a route mounted for every method needs the CSRF token for
	// anything but GET and HEAD.
	Session
	// Mutation routes change something: they need a signed-in user and,
	// like every write, the session's CSRF token.
	Mutation
	// Admin routes need an administrator, and the CSRF token for anything
	// but GET and HEAD.
	Admin
)

func (p Posture) String() string {
	switch p {
	case Public:
		return "public"
	case Session:
		return "session"
	case Mutation:
		return "mutation"
	case Admin:
		return "admin"
	}
	return "unknown"
}

// Handler serves a request for the session's user. Public routes receive the
// zero Session; for the others the request's context carries the session
// token (accounts.SessionToken), which the File Broker checks itself.
type Handler func(http.ResponseWriter, *http.Request, accounts.Session)

// Route is one entry of the route table.
type Route struct {
	// Method is empty for an API mounted under Pattern, which serves every
	// method itself.
	Method  string
	Pattern string
	Posture Posture
	Handler Handler
}

// Safe reports whether a method only reads.
func Safe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// Mount mounts an API that serves every method under prefix, both the
// prefix itself and the paths beneath it.
func Mount(prefix string, posture Posture, handler Handler) []Route {
	return []Route{
		{Pattern: prefix, Posture: posture, Handler: handler},
		{Pattern: prefix + "/", Posture: posture, Handler: handler},
	}
}
