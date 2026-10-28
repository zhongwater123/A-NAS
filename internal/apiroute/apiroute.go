// Package apiroute declares the product API's routes as data. Each route
// names who may call it, its posture, and internal/httpapi checks that
// before the handler runs, so a handler never decides on its own whether a
// session, the CSRF token or an administrator is needed (#192).
//
// A domain's HTTP package (internal/<domain>api) returns its routes from a
// Routes method; the Product Service puts them into one table with its own.
// An API that also runs in a service of its own, or that the Product Service
// otherwise hands every request beneath a prefix, registers its operations
// on a Mux and lists them as Endpoints, which the table then expands (#273).
package apiroute

import (
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
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
	// Endpoints are the operations a route without a method serves: those
	// at Pattern, or beneath it when it ends in a slash.
	Endpoints []Endpoint
}

// Endpoint is one operation of a mounted API: a method and a full pattern
// of http.ServeMux.
type Endpoint struct {
	Method  string
	Pattern string
}

// Safe reports whether a method only reads.
func Safe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// Mount mounts an API that serves every method under prefix and handles the
// endpoints of its own there: the prefix itself when one of them is the
// prefix, and the paths beneath it.
func Mount(prefix string, posture Posture, handler Handler, endpoints []Endpoint) []Route {
	at := Route{Pattern: prefix, Posture: posture, Handler: handler}
	beneath := Route{Pattern: prefix + "/", Posture: posture, Handler: handler}
	for _, endpoint := range endpoints {
		switch {
		case endpoint.Pattern == prefix:
			at.Endpoints = append(at.Endpoints, endpoint)
		case strings.HasPrefix(endpoint.Pattern, beneath.Pattern):
			beneath.Endpoints = append(beneath.Endpoints, endpoint)
		}
	}
	routes := []Route{beneath}
	if len(at.Endpoints) > 0 {
		routes = append(routes, at)
	}
	return routes
}

// Serves reports whether a route without a method reaches endpoint.
func (r Route) Serves(endpoint Endpoint) bool {
	if strings.HasSuffix(r.Pattern, "/") {
		return strings.HasPrefix(endpoint.Pattern, r.Pattern)
	}
	return endpoint.Pattern == r.Pattern
}

// Expand lists every operation of the table as a route with a method: the
// routes that have one as they are, and in place of each mounted API its
// endpoints with the posture its mount enforces, which makes the writes of
// a Session API Mutations.
func Expand(routes []Route) []Route {
	var expanded []Route
	for _, route := range routes {
		if route.Method != "" {
			expanded = append(expanded, route)
			continue
		}
		for _, endpoint := range route.Endpoints {
			posture := route.Posture
			if posture == Session && !Safe(endpoint.Method) {
				posture = Mutation
			}
			expanded = append(expanded, Route{Method: endpoint.Method, Pattern: endpoint.Pattern, Posture: posture, Handler: route.Handler})
		}
	}
	return expanded
}

// Mux is an http.ServeMux that lists the operations registered on it as
// Endpoints. Patterns without a method, such as a catch-all answering
// not_found, are not operations; neither is what a package registers on the
// embedded ServeMux directly.
type Mux struct {
	http.ServeMux
	endpoints []Endpoint
}

// NewMux returns an empty Mux.
func NewMux() *Mux { return &Mux{} }

// Handle registers handler for pattern.
func (m *Mux) Handle(pattern string, handler http.Handler) {
	if method, path, ok := strings.Cut(pattern, " "); ok {
		m.endpoints = append(m.endpoints, Endpoint{Method: method, Pattern: path})
	}
	m.ServeMux.Handle(pattern, handler)
}

// HandleFunc registers handler for pattern.
func (m *Mux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(handler))
}

// Only registers handler for one method at path and answers any other
// method there with 405 method_not_allowed.
func (m *Mux) Only(method, path string, handler func(http.ResponseWriter, *http.Request)) {
	m.HandleFunc(method+" "+path, handler)
	m.ServeMux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", method)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
}

// Endpoints lists the operations registered with a method, in order.
func (m *Mux) Endpoints() []Endpoint {
	return append([]Endpoint(nil), m.endpoints...)
}
