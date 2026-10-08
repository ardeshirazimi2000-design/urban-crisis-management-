package httpx

import (
	"net/http"
	"strings"
)

// Router registers versioned routes under a prefix, separating public and authenticated handlers.
type Router struct {
	Mux    *http.ServeMux
	Prefix string // e.g. /api/v1
	AuthMW Middleware
	// Routes records every registered "METHOD /path" (used by the OpenAPI contract test).
	Routes []string
}

func (r *Router) path(pattern string) string {
	method, p, ok := strings.Cut(pattern, " ")
	if !ok {
		return r.Prefix + pattern
	}
	return method + " " + r.Prefix + p
}

// Auth registers a handler that requires an authenticated principal.
func (r *Router) Auth(pattern string, h HandlerFunc) {
	r.Routes = append(r.Routes, r.path(pattern))
	r.Mux.Handle(r.path(pattern), r.AuthMW(Handle(h)))
}

// Public registers a handler without authentication (health, signed media URLs).
func (r *Router) Public(pattern string, h HandlerFunc) {
	r.Routes = append(r.Routes, r.path(pattern))
	r.Mux.Handle(r.path(pattern), Handle(h))
}
