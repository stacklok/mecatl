package mcpbroker

import "net/http"

// HTTPHandlers is the fixed process-owned HTTP surface of an in-process broker.
// Command roots choose the listener; the implementation owns the exact route
// set. A broker with no HTTP surface provides a nil HTTPHandlers.
type HTTPHandlers interface {
	// Mount registers the complete route set and the callback path selected by
	// trusted composition. It rejects incomplete surfaces and route collisions
	// before mutating mux.
	Mount(mux *http.ServeMux, callbackPath string) error
}
