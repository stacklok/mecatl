package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestOpenRouterRouteE2E drives the FULL composition (app.Build → server.Service →
// the REAL openai adapter) against an httptest OpenRouter-shaped server, offline.
// Unlike TestMultiProviderE2E (which uses the providerConstructor mock seam), this
// leaves providerConstructor NIL so the openrouter entry builds the REAL openai
// adapter — the only way to prove the `provider` body object + X-OpenRouter-Metadata
// header actually land on the wire and the routed downstream echoes back as
// EvProviderRoute (issue #480). It proves:
//
//  1. The operator-tier openrouter: config stamps provider.order + allow_fallbacks
//     onto the request body for the configured model, and arms the metadata header.
//  2. The run stream carries EvProviderRoute with the selected downstream slug
//     (parsed from the SSE openrouter_metadata).
//  3. A cache-hit response (no openrouter_metadata) yields NO EvProviderRoute.
//  4. The plain openai entry pointed at the SAME server sends NEITHER the body key
//     NOR the header (the knobs are gated to the openrouter entry).
func TestOpenRouterRouteE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	const model = "anthropic/claude-sonnet-4-6"

	// captured records the last request's body + metadata header per path.
	type capture struct {
		body        map[string]any
		metadataHdr string
	}
	var caps []capture
	completed := func(meta string) string {
		return "event: response.output_text.delta\n" +
			`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"hello"}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}` + meta + `}}` + "\n\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var c capture
		c.metadataHdr = r.Header.Get("X-OpenRouter-Metadata")
		if err := json.Unmarshal(raw, &c.body); err != nil {
			t.Errorf("unmarshal body: %v (body=%s)", err, raw)
		}
		caps = append(caps, c)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Serve the metadata block only when the request armed the header (the
		// openrouter path); the openai-parity path gets a cache-hit (no metadata).
		if c.metadataHdr == "enabled" {
			_, _ = w.Write([]byte(completed(`,"openrouter_metadata":{"endpoints":{"available":[{"provider":"Anthropic","selected":true}]},"attempts":[{"provider":"Anthropic"}]}`)))
		} else {
			_, _ = w.Write([]byte(completed("")))
		}
	}))
	defer srv.Close()

	// Operator-tier openrouter: config via an explicit permconfig file.
	orYAML := "openrouter:\n  models:\n    \"" + model + "\":\n      order: [\"anthropic\", \"google-vertex\"]\n      allow_fallbacks: false\n"
	orPath := filepath.Join(t.TempDir(), "openrouter.yaml")
	if err := os.WriteFile(orPath, []byte(orYAML), 0o600); err != nil {
		t.Fatalf("write openrouter config: %v", err)
	}

	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenRouter: {BaseURL: srv.URL + "/v1"},
			providerOpenAI:     {BaseURL: srv.URL + "/v1"},
		},
		envDetector: fakeEnv(map[string]string{
			"OPENROUTER_API_KEY": "sk-test",
			"OPENAI_API_KEY":     "sk-test",
		}),
		liveModelHTTPClient: offlineHTTPClient(),
		// The operator-tier openrouter: block the fold reads.
		PermissionConfigs: []string{orPath},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	// --- Leg 1: openrouter session on the configured model ---
	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter, ModelID: model})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(openrouter): %v", err)
	}
	run, err := svc.StartRun(ctx, sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun(openrouter): %v", err)
	}
	var routes []string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvProviderRoute {
			routes = append(routes, ev.Text)
		}
	}
	if len(routes) != 1 || routes[0] != "Anthropic" {
		t.Fatalf("openrouter run EvProviderRoute = %v, want [Anthropic]", routes)
	}
	// The request carried the provider object + header.
	if len(caps) == 0 {
		t.Fatal("no request captured for the openrouter leg")
	}
	orReq := caps[len(caps)-1]
	prov, ok := orReq.body["provider"].(map[string]any)
	if !ok {
		t.Fatalf("openrouter request missing provider object; body=%v", orReq.body)
	}
	order, _ := prov["order"].([]any)
	if len(order) != 2 || order[0] != "anthropic" || order[1] != "google-vertex" {
		t.Errorf("provider.order = %v, want [anthropic google-vertex]", prov["order"])
	}
	if af, _ := prov["allow_fallbacks"].(bool); af != false {
		t.Errorf("provider.allow_fallbacks = %v, want false", prov["allow_fallbacks"])
	}
	if orReq.metadataHdr != "enabled" {
		t.Errorf("openrouter X-OpenRouter-Metadata = %q, want enabled", orReq.metadataHdr)
	}

	// --- Leg 2: openai parity — same server, no openrouter knobs ---
	caps = caps[:0]
	sess2, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(openai): %v", err)
	}
	run2, err := svc.StartRun(ctx, sess2.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun(openai): %v", err)
	}
	for ev := range run2.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run2.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvProviderRoute {
			t.Fatalf("openai parity leg emitted EvProviderRoute %q, want none", ev.Text)
		}
	}
	if len(caps) == 0 {
		t.Fatal("no request captured for the openai parity leg")
	}
	oaReq := caps[len(caps)-1]
	if _, present := oaReq.body["provider"]; present {
		t.Errorf("openai parity request stamped a provider key; body=%v", oaReq.body)
	}
	if oaReq.metadataHdr != "" {
		t.Errorf("openai parity sent X-OpenRouter-Metadata=%q, want empty", oaReq.metadataHdr)
	}
}

// TestOpenRouterRouteE2ECacheHit pins the honest degradation end-to-end: when the
// (openrouter) response carries NO openrouter_metadata (a cache hit), the run emits
// NO EvProviderRoute — never a fabricated value.
func TestOpenRouterRouteE2ECacheHit(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Cache-hit: completed with no openrouter_metadata, even though the header
		// was armed. A text delta makes it a clean one-turn end (no nudge re-fires).
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"hello"}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"))
	}))
	defer srv.Close()

	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenRouter: {BaseURL: srv.URL + "/v1"},
		},
		envDetector:         fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-test"}),
		liveModelHTTPClient: offlineHTTPClient(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter, ModelID: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	run, err := svc.StartRun(ctx, sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvProviderRoute {
			t.Fatalf("cache-hit run emitted EvProviderRoute %q, want none", ev.Text)
		}
	}
}

// TestOpenRouterRouteForNilSafety is a compile-time guard that the permconfig import
// is genuinely used in this file's package (the fold tests live in
// openrouter_route_test.go; this keeps the e2e file's permconfig reference honest).
var _ = permconfig.OpenRouterSection{}
