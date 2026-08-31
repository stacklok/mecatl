package mcp

import (
	"strings"
	"testing"
)

func TestManagerSelectedToolsIsClosedDirectView(t *testing.T) {
	first := connectTest(t, ServerConfig{Name: "github", URL: newTestServer(t, nil)})
	second := connectTest(t, ServerConfig{Name: "slack", URL: newTestServer(t, nil)})
	manager := &Manager{servers: []*Server{first, second}}

	selected, err := manager.SelectedTools([]string{"github"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 3 {
		t.Fatalf("selected tools=%d want 3", len(selected))
	}
	for _, candidate := range selected {
		if !strings.HasPrefix(candidate.Spec().Name, "mcp__github__") {
			t.Fatalf("unselected tool mounted: %s", candidate.Spec().Name)
		}
		if candidate.Spec().Name == "CallMcpWithQuery" {
			t.Fatal("resource/query meta-tool escaped selected direct view")
		}
	}

	ceiling := make([]string, 0, len(selected))
	for _, candidate := range selected {
		ceiling = append(ceiling, candidate.Spec().Name)
	}
	bounded, err := manager.SelectedTools([]string{"github"}, ceiling)
	if err != nil || len(bounded) != len(ceiling) {
		t.Fatalf("bounded=%v err=%v", bounded, err)
	}
	if _, err := manager.SelectedTools([]string{"github"}, ceiling[:1]); err == nil {
		t.Fatal("persisted ceiling missing current additions was accepted")
	}
	if _, err := manager.SelectedTools([]string{"missing"}, nil); err == nil {
		t.Fatal("unknown server accepted")
	}
	if _, err := manager.SelectedTools([]string{"github"}, []string{"mcp__github__new_tool"}); err == nil {
		t.Fatal("missing persisted ceiling tool accepted")
	}
}
