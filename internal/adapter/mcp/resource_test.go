package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestListResourcesPerServerAndAll(t *testing.T) {
	url, _ := newTestServer(t, nil)

	url2, _ := newTestServer(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := NewManager(ctx, []ServerConfig{
		{Name: "a", URL: url},
		{Name: "b", URL: url2},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Per-server.
	a, err := m.ListResources(ctx, "a")
	if err != nil {
		t.Fatalf("ListResources(a): %v", err)
	}
	if len(a) != 2 {
		t.Fatalf("server a resources = %d, want 2 (text+binary)", len(a))
	}
	for _, r := range a {
		if r.Server != "a" {
			t.Errorf("resource server = %q, want a", r.Server)
		}
		if !r.ReadOnly {
			t.Errorf("resource %q ReadOnly = false, want true", r.URI)
		}
	}

	// All servers (empty name → union).
	all, err := m.ListResources(ctx, "")
	if err != nil {
		t.Fatalf("ListResources(all): %v", err)
	}
	if len(all) != 4 {
		t.Errorf("all resources = %d, want 4 (2 per server)", len(all))
	}

	// Unknown server → error.
	if _, err := m.ListResources(ctx, "nope"); err == nil {
		t.Errorf("ListResources(unknown) = nil error, want error")
	}
}

func TestReadResourceText(t *testing.T) {
	url, _ := newTestServer(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := NewManager(ctx, []ServerConfig{{Name: "a", URL: url}}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	c, err := m.ReadResource(ctx, "a", "test://text")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if c.Text != "hello resource" {
		t.Errorf("text = %q, want %q", c.Text, "hello resource")
	}
}

func TestReadResourceBinarySummarized(t *testing.T) {
	url, _ := newTestServer(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := NewManager(ctx, []ServerConfig{{Name: "a", URL: url}}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	c, err := m.ReadResource(ctx, "a", "test://binary")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if !strings.HasPrefix(c.Text, "[binary resource:") {
		t.Errorf("binary read = %q, want a [binary resource: ...] summary", c.Text)
	}
	if !strings.Contains(c.Text, "image/png") || !strings.Contains(c.Text, "4 bytes") {
		t.Errorf("binary summary = %q, want mime + byte count", c.Text)
	}
	// Must NOT contain the raw bytes / base64 dump.
	if strings.Contains(c.Text, "iVBOR") {
		t.Errorf("binary summary leaked base64: %q", c.Text)
	}
}

func TestReadResourceUnknownURIErrors(t *testing.T) {
	url, _ := newTestServer(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := NewManager(ctx, []ServerConfig{{Name: "a", URL: url}}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Provider-level read of an unknown URI is a Go error (the tool layer turns it
	// into a model-facing tool error; see restool_test.go).
	if _, err := m.ReadResource(ctx, "a", "test://does-not-exist"); err == nil {
		t.Errorf("ReadResource(unknown URI) = nil error, want error")
	}
}

func TestCapabilityAbsentServerSkipped(t *testing.T) {
	url, _ := newToolsOnlyServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := NewManager(ctx, []ServerConfig{{Name: "t", URL: url}}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	res, err := m.ListResources(ctx, "t")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("tools-only server resources = %d, want 0", len(res))
	}
	pr, err := m.ListPrompts(ctx, "t")
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(pr) != 0 {
		t.Errorf("tools-only server prompts = %d, want 0", len(pr))
	}
	// Its tool is still usable.
	if len(m.Tools()) != 1 {
		t.Errorf("tools-only server tools = %d, want 1", len(m.Tools()))
	}
}
