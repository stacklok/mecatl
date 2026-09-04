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
	pattern string
	handler http.Handler
	methods []string
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
		mux.Handle(route.pattern, route.handler)
	}
	return nil
}

func (h HandlerBundle) routes(callbackPath string) ([]handlerRoute, error) {
	fixed := []handlerRoute{
		{path: toolHiveBasePath + "/oauth/authorize", pattern: toolHiveBasePath + "/oauth/authorize", handler: h.Authorization},
		{path: toolHiveBasePath + "/oauth/token", pattern: toolHiveBasePath + "/oauth/token", handler: h.Token},
		{path: toolHiveBasePath + "/oauth/callback", pattern: toolHiveBasePath + "/oauth/callback", handler: h.UpstreamCallback},
		{path: toolHiveBasePath + "/.well-known/openid-configuration", pattern: toolHiveBasePath + "/.well-known/openid-configuration", handler: h.Discovery},
		{path: toolHiveBasePath + "/.well-known/jwks.json", pattern: toolHiveBasePath + "/.well-known/jwks.json", handler: h.JWKS},
		{path: toolHiveBasePath + "/.well-known/oauth-protected-resource", pattern: toolHiveBasePath + "/.well-known/oauth-protected-resource", handler: h.ProtectedResource},
		{path: toolHiveMCPPath, pattern: toolHiveMCPPath, handler: h.VMCP},
	}
	if err := protectedHandlersComplete(fixed[:6]); err != nil {
		return nil, err
	}
	if h.VMCP != nil {
		for _, route := range fixed[:6] {
			if route.handler == nil {
				return nil, errors.New("mcpbroker: vMCP handler requires the protected authorization bundle")
			}
		}
	}
	routes := nonNilRoutes(fixed)
	if callbackPath != "" || h.Callback != nil {
		if err := validCallbackHandler(callbackPath, h.Callback); err != nil {
			return nil, err
		}
		route := handlerRoute{path: callbackPath, pattern: callbackPath, handler: h.Callback}
		if callbackPath == "/" {
			route.pattern = "GET /{$}"
			route.methods = []string{http.MethodGet}
		}
		routes = append(routes, route)
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
	if handler == nil || callbackPath == "" ||
		!strings.HasPrefix(callbackPath, "/") || path.Clean(callbackPath) != callbackPath {
		return fmt.Errorf("mcpbroker: invalid callback path %q", callbackPath)
	}
	return nil
}

func uniqueHandlerRoutes(routes []handlerRoute) error {
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if _, exists := seen[route.pattern]; exists {
			return fmt.Errorf("mcpbroker: handler route conflict %q", route.path)
		}
		seen[route.pattern] = struct{}{}
	}
	return nil
}

func registeredHandlerRouteConflict(mux *http.ServeMux, routes []handlerRoute) error {
	allMethods := [...]string{
		http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace,
	}
	for _, route := range routes {
		methods := route.methods
		if len(methods) == 0 {
			methods = allMethods[:]
		}
		for _, method := range methods {
			request := &http.Request{Method: method, URL: &url.URL{Path: route.path}}
			_, pattern := mux.Handler(request)
			// The command root's plain "/" API fallback is intentionally
			// superseded by exact broker routes. Any other matching route would
			// shadow or be shadowed by one method of the broker endpoint.
			if pattern != "" && pattern != "/" {
				return fmt.Errorf("mcpbroker: handler route conflict %q", route.path)
			}
		}
	}
	return nil
}

func callbackPath(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.EscapedPath() != parsed.Path || (parsed.Path != "" && path.Clean(parsed.Path) != parsed.Path) {
		return "", fmt.Errorf("mcpbroker: invalid callback URL")
	}
	if parsed.Path == "" {
		return "/", nil
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
