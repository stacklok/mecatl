package server_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// soulService builds a Service carrying the given soul snapshot, mirroring
// skillsService but for the GetSoul RPC's snapshot field. A nil snapshot means no
// soul source is wired (capabilities().Soul false; GetSoul returns an empty,
// present=false SoulInfo).
func soulService(t *testing.T, snapshot *mecatlv1.SoulInfo) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:  func() time.Time { return time.Unix(0, 0) },
		Soul: snapshot,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func cannedSoul() *mecatlv1.SoulInfo {
	return &mecatlv1.SoulInfo{
		Content:    "You are terse and direct.",
		SizeBytes:  25,
		Sha256:     "abc123",
		Present:    true,
		Provenance: mecatlv1.SoulProvenance_SOUL_PROVENANCE_USER,
		Trusted:    true,
		Drifted:    false,
	}
}

func TestGRPCGetSoul(t *testing.T) {
	svc := soulService(t, cannedSoul())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.GetSoul(context.Background(), &mecatlv1.GetSoulRequest{})
	if err != nil {
		t.Fatalf("GetSoul: %v", err)
	}
	got := resp.GetSoul()
	if got == nil {
		t.Fatal("soul is nil")
	}
	if !got.GetPresent() {
		t.Error("present = false, want true")
	}
	if got.GetContent() != "You are terse and direct." {
		t.Errorf("content = %q", got.GetContent())
	}
	if got.GetProvenance() != mecatlv1.SoulProvenance_SOUL_PROVENANCE_USER {
		t.Errorf("provenance = %v, want USER", got.GetProvenance())
	}
	if !got.GetTrusted() {
		t.Error("trusted = false, want true (user soul)")
	}
}

func TestGRPCGetSoulEmpty(t *testing.T) {
	// No snapshot wired (soul disabled) => empty, present=false, never nil.
	svc := soulService(t, nil)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.GetSoul(context.Background(), &mecatlv1.GetSoulRequest{})
	if err != nil {
		t.Fatalf("GetSoul: %v", err)
	}
	if resp.GetSoul() == nil {
		t.Fatal("soul must never be nil over the wire")
	}
	if resp.GetSoul().GetPresent() {
		t.Error("present = true, want false (no soul wired)")
	}
}

func TestHTTPGetSoul(t *testing.T) {
	svc := soulService(t, cannedSoul())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	var resp mecatlv1.GetSoulResponse
	if code := httpGet(t, srv, "/v1/soul", &resp); code != 200 {
		t.Fatalf("GET /v1/soul status = %d", code)
	}
	if !resp.GetSoul().GetPresent() || resp.GetSoul().GetContent() != "You are terse and direct." {
		t.Fatalf("http soul = %+v", resp.GetSoul())
	}
}

// TestSoulCapability asserts the soul cap flips with a wired snapshot.
func TestSoulCapability(t *testing.T) {
	on := capsFromCreate(t, soulService(t, cannedSoul()))
	if !on.GetSoul() {
		t.Error("soul cap = false, want true (snapshot wired)")
	}
	off := capsFromCreate(t, soulService(t, nil))
	if off.GetSoul() {
		t.Error("soul cap = true, want false (no snapshot)")
	}
}
