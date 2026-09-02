package mcpbroker

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// HandlerBundle is the fixed process-owned HTTP surface of an in-process broker.
// Command roots choose the listener, but the adapter owns the exact route set.
type HandlerBundle struct {
	Authorization     http.Handler
	Token             http.Handler
	UpstreamCallback  http.Handler
	Discovery         http.Handler
	JWKS              http.Handler
	ProtectedResource http.Handler
	VMCP              http.Handler
	Callback          http.Handler
}

// Empty reports whether the process exposes no HTTP surface.
func (h HandlerBundle) Empty() bool {
	return h.Authorization == nil && h.Token == nil && h.UpstreamCallback == nil &&
		h.Discovery == nil && h.JWKS == nil && h.ProtectedResource == nil &&
		h.VMCP == nil && h.Callback == nil
}

type handlerRoute struct {
	path    string
	handler http.Handler
}

// Mount registers the complete fixed route set and the callback path selected
// by trusted composition. It rejects incomplete protected bundles and route
// collisions before mutating the mux.
func (h HandlerBundle) Mount(mux *http.ServeMux, callbackPath string) (err error) {
	if mux == nil {
		return errors.New("mcpbroker: handler mux is required")
	}
	routes, err := h.routes(callbackPath)
	if err != nil {
		return err
	}
	if err := registeredHandlerRouteConflict(mux, routes); err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("mcpbroker: mount handler routes: %v", recovered)
		}
	}()
	for _, route := range routes {
		mux.Handle(route.path, route.handler)
	}
	return nil
}

func (h HandlerBundle) routes(callbackPath string) ([]handlerRoute, error) {
	fixed := []handlerRoute{
		{toolHiveBasePath + "/oauth/authorize", h.Authorization},
		{toolHiveBasePath + "/oauth/token", h.Token},
		{toolHiveBasePath + "/oauth/callback", h.UpstreamCallback},
		{toolHiveBasePath + "/.well-known/openid-configuration", h.Discovery},
		{toolHiveBasePath + "/.well-known/jwks.json", h.JWKS},
		{toolHiveBasePath + "/.well-known/oauth-protected-resource", h.ProtectedResource},
		{toolHiveMCPPath, h.VMCP},
	}
	if err := protectedHandlersComplete(fixed[:6]); err != nil {
		return nil, err
	}
	routes := nonNilRoutes(fixed)
	if callbackPath != "" || h.Callback != nil {
		if err := validCallbackHandler(callbackPath, h.Callback); err != nil {
			return nil, err
		}
		routes = append(routes, handlerRoute{callbackPath, h.Callback})
	}
	if len(routes) == 0 {
		return nil, errors.New("mcpbroker: incomplete handler bundle")
	}
	if err := uniqueHandlerRoutes(routes); err != nil {
		return nil, err
	}
	return routes, nil
}

func protectedHandlersComplete(routes []handlerRoute) error {
	protected := false
	for _, route := range routes {
		protected = protected || route.handler != nil
	}
	if !protected {
		return nil
	}
	for _, route := range routes {
		if route.handler == nil {
			return errors.New("mcpbroker: incomplete protected handler bundle")
		}
	}
	return nil
}

func nonNilRoutes(in []handlerRoute) []handlerRoute {
	out := make([]handlerRoute, 0, len(in))
	for _, route := range in {
		if route.handler != nil {
			out = append(out, route)
		}
	}
	return out
}

func validCallbackHandler(callbackPath string, handler http.Handler) error {
	if handler == nil || callbackPath == "" || callbackPath == "/" ||
		!strings.HasPrefix(callbackPath, "/") || path.Clean(callbackPath) != callbackPath {
		return fmt.Errorf("mcpbroker: invalid callback path %q", callbackPath)
	}
	return nil
}

func uniqueHandlerRoutes(routes []handlerRoute) error {
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if _, exists := seen[route.path]; exists {
			return fmt.Errorf("mcpbroker: handler route conflict %q", route.path)
		}
		seen[route.path] = struct{}{}
	}
	return nil
}

func registeredHandlerRouteConflict(mux *http.ServeMux, routes []handlerRoute) error {
	for _, route := range routes {
		request := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: route.path}}
		_, pattern := mux.Handler(request)
		if pattern == route.path {
			return fmt.Errorf("mcpbroker: handler route conflict %q", route.path)
		}
	}
	return nil
}

func callbackPath(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path == "" || parsed.Path == "/" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.EscapedPath() != parsed.Path || path.Clean(parsed.Path) != parsed.Path {
		return "", fmt.Errorf("mcpbroker: invalid callback URL")
	}
	return parsed.Path, nil
}

// Handlers returns the fixed callback bundle and its exact mount path.
func (r *Runtime) Handlers(callbackURL string) (HandlerBundle, string, error) {
	callbackPath, err := callbackPath(callbackURL)
	if err != nil {
		return HandlerBundle{}, "", err
	}
	return HandlerBundle{Callback: r.CallbackHandler()}, callbackPath, nil
}
