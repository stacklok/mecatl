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
// Public authorization controls are deliberately not part of this P10 bundle.
type HandlerBundle struct {
	Callback http.Handler
}

// Mount registers the complete bundle at the callback path selected by trusted
// composition. It rejects non-canonical paths and route collisions.
func (h HandlerBundle) Mount(mux *http.ServeMux, callbackPath string) (err error) {
	if mux == nil || h.Callback == nil {
		return errors.New("mcpbroker: incomplete handler bundle")
	}
	if callbackPath == "" || callbackPath == "/" || !strings.HasPrefix(callbackPath, "/") || path.Clean(callbackPath) != callbackPath {
		return fmt.Errorf("mcpbroker: invalid callback path %q", callbackPath)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("mcpbroker: mount callback path %q: %v", callbackPath, recovered)
		}
	}()
	mux.Handle(callbackPath, h.Callback)
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
