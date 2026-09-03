package mcpbroker

import (
	"net/http"
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
