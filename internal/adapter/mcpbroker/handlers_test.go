package mcpbroker

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerBundleMountsExactCallbackAndRejectsCollision(t *testing.T) {
	bundle := HandlerBundle{Callback: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	mux := http.NewServeMux()
	if err := bundle.Mount(mux, "/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Mount(mux, "/oauth/callback"); err == nil {
		t.Fatal("duplicate callback mount succeeded")
	}
	if err := bundle.Mount(http.NewServeMux(), "/oauth/../callback"); err == nil {
		t.Fatal("non-canonical callback path succeeded")
	}
	if err := bundle.Mount(http.NewServeMux(), "/"); err == nil {
		t.Fatal("catch-all callback path succeeded")
	}
}

func TestCallbackPathRejectsEscapedOrDecoratedURL(t *testing.T) {
	for _, raw := range []string{"https://agent.example/", "https://agent.example/oauth/%63allback", "https://agent.example/oauth/callback?x=1", "https://agent.example/oauth/../callback"} {
		if _, err := callbackPath(raw); err == nil {
			t.Errorf("callbackPath(%q) succeeded", raw)
		}
	}
	if got, err := callbackPath("https://agent.example/oauth/callback"); err != nil || got != "/oauth/callback" {
		t.Fatalf("callbackPath = %q, %v", got, err)
	}
}

func TestHandlerBundleMountRejectsExistingRouteWithoutPartialMount(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	bundle := HandlerBundle{
		Authorization:     handler,
		Token:             handler,
		UpstreamCallback:  handler,
		Discovery:         handler,
		JWKS:              handler,
		ProtectedResource: handler,
		VMCP:              handler,
		Callback:          handler,
	}
	const callbackPath = "/agent/callback"

	mux := http.NewServeMux()
	mux.Handle(toolHiveBasePath+"/oauth/token", handler)
	if err := bundle.Mount(mux, callbackPath); err == nil {
		t.Fatal("mount with an existing route succeeded")
	}

	routes, err := bundle.routes(callbackPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.path == toolHiveBasePath+"/oauth/token" {
			continue
		}
		request, err := http.NewRequest(http.MethodGet, "http://broker.example"+route.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, pattern := mux.Handler(request); pattern != "" {
			t.Errorf("route %q was registered despite the collision", route.path)
		}
	}
}

func TestHandlerBundleMountRejectsEachMissingProtectedHandlerWithoutRoutes(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	missing := []struct {
		name string
		set  func(*HandlerBundle)
	}{
		{"authorization", func(h *HandlerBundle) { h.Authorization = nil }},
		{"token", func(h *HandlerBundle) { h.Token = nil }},
		{"upstream callback", func(h *HandlerBundle) { h.UpstreamCallback = nil }},
		{"discovery", func(h *HandlerBundle) { h.Discovery = nil }},
		{"jwks", func(h *HandlerBundle) { h.JWKS = nil }},
		{"protected resource", func(h *HandlerBundle) { h.ProtectedResource = nil }},
	}
	for _, test := range missing {
		t.Run(test.name, func(t *testing.T) {
			bundle := HandlerBundle{
				Authorization: handler, Token: handler, UpstreamCallback: handler, Discovery: handler,
				JWKS: handler, ProtectedResource: handler, VMCP: handler, Callback: handler,
			}
			test.set(&bundle)
			mux := http.NewServeMux()
			if err := bundle.Mount(mux, "/agent/callback"); err == nil {
				t.Fatal("mount with a missing protected handler succeeded")
			}
			for _, route := range []string{
				toolHiveBasePath + "/oauth/authorize", toolHiveBasePath + "/oauth/token", toolHiveBasePath + "/oauth/callback",
				toolHiveBasePath + "/.well-known/openid-configuration", toolHiveBasePath + "/.well-known/jwks.json",
				toolHiveBasePath + "/.well-known/oauth-protected-resource", toolHiveMCPPath, "/agent/callback",
			} {
				request := httptest.NewRequest(http.MethodGet, "http://broker.example"+route, nil)
				if _, pattern := mux.Handler(request); pattern != "" {
					t.Errorf("route %q was registered despite incomplete protected bundle", route)
				}
			}
		})
	}
}
