package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func browserInventory() *fakeMCP {
	f := sampleResourceMCP()
	f.resources, f.prompts = nil, nil
	for i := range 60 {
		label := fmt.Sprintf("item-%02d\twith\ncontinuation\x1b[31m", i)
		f.resources = append(f.resources, client.MCPResource{Server: "server\tname", URI: fmt.Sprintf("raw:%d", i), Name: label})
		f.prompts = append(f.prompts, client.MCPPrompt{Server: "server\tname", Name: label})
	}
	return f
}

func TestMCPBrowserRoutedNavigationWheelAndCompact(t *testing.T) {
	for _, resources := range []bool{true, false} {
		t.Run(fmt.Sprint(resources), func(t *testing.T) {
			f := browserInventory()
			m := newMCPModel(t, aztec(), f)
			m.keys.ScrollBottom = key.NewBinding(key.WithKeys("G"))
			m.keys.ScrollTop = key.NewBinding(key.WithKeys("g"))
			m.keys.ScrollD = key.NewBinding(key.WithKeys("f"))
			open := tea.KeyPressMsg{Code: tea.KeyF8}
			if resources {
				open = ctrlKey('r')
			}
			m = openOverlay(t, m, open)
			s := mcpActive(m)
			list := &s.prList
			if resources {
				list = &s.resList
			}
			m.prompt.Rewrite("draft")
			m.vp.SetContent(strings.Repeat("hidden conversation\n", 100))
			m.vp.SetYOffset(8)
			offset := m.vp.YOffset()
			_ = m.View()
			dimensions := *m.metrics
			selected := list.CursorID()
			for range 6 {
				m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			}
			_ = m.View()
			if list.CursorID() != selected || list.Offset() == 0 || m.vp.YOffset() != offset || m.prompt.Value() != "draft" {
				t.Fatal("wheel selected, failed to browse, or escaped the modal")
			}
			if *m.metrics != dimensions {
				t.Fatal("wheel resized card")
			}
			saved := list.Offset()
			m = applyAll(m, tea.WindowSizeMsg{Width: 8, Height: 3})
			for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyDown}, tea.PasteMsg{Content: "hidden"}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 1}} {
				mm, cmd := m.Update(msg)
				m = mm.(Model)
				if cmd != nil {
					t.Fatalf("compact input %T produced command", msg)
				}
			}
			if !s.compact || list.Offset() != saved || list.CursorID() != selected || s.loading {
				t.Fatal("compact input mutated inventory")
			}
			m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
			_ = m.View()
			if list.Offset() != saved {
				t.Fatal("compact restoration reset wheel window")
			}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
			if !strings.Contains(stripANSIstr(m.renderBody()), "▶ item-01") {
				t.Fatal("keyboard did not reveal selection")
			}
			m = applyAll(m, tea.KeyPressMsg{Code: 'f', Text: "f"})
			if list.Cursor() <= 1 {
				t.Fatal("page key did not move")
			}
			m = applyAll(m, tea.KeyPressMsg{Code: 'G', Text: "G"})
			if list.Cursor() != 59 {
				t.Fatal("end key did not select last identity")
			}
			_ = m.View()
			if *m.metrics != dimensions {
				t.Fatal("selection resized card")
			}
			m, cmd := pickerKey(m, tea.KeyEnter)
			if resources {
				m = applyAll(m, runCmd(cmd))
				if s.previewResource.URI != "raw:59" {
					t.Fatal("wrong resource activated")
				}
			} else {
				receipt := runCmd(cmd).(mcpPickerResult)
				got := receipt.result.(client.MCPPromptGotMsg)
				if got.Name != f.prompts[59].Name {
					t.Fatal("wrong raw prompt activated")
				}
			}
		})
	}
}

func TestMCPBrowserOffersAndStableBounds(t *testing.T) {
	for _, resources := range []bool{true, false} {
		for _, size := range [][2]int{{160, 50}, {24, 18}, {80, 8}, {1, 1}, {0, 0}, {-1, -1}} {
			m := newMCPModel(t, aztec(), browserInventory())
			open := tea.KeyPressMsg{Code: tea.KeyF8}
			if resources {
				open = ctrlKey('r')
			}
			m = openOverlay(t, m, open)
			m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			_ = m.View()
			s := mcpActive(m)
			if m.width <= 0 || m.vp.Height() <= 0 {
				if !s.compact {
					t.Fatalf("%t %v: unusable root offer did not compact", resources, size)
				}
				continue
			}
			b := m.metrics.outerBounds
			if b.x0 < 0 || b.x1 > m.width || b.y0 < convTopRow(m) || b.y1 > convTopRow(m)+m.vp.Height() {
				t.Fatalf("%t %v: root-rendered bounds %+v outside conversation region %dx%d", resources, size, b, m.width, m.vp.Height())
			}
			if !s.compact && b.x1-b.x0 > min(128, m.width) {
				t.Fatalf("%t %v: framed root card exceeds width cap: %+v", resources, size, b)
			}
			raw := m.renderBody()
			if strings.Contains(raw, "\x1b[31m") {
				t.Fatal("untrusted list escape survived styling")
			}
			if plain := stripANSIstr(raw); strings.ContainsAny(plain, "\t\x1b\x00") {
				t.Fatal("unsafe list display")
			}
		}
	}
}

func TestMCPBrowserRawIdentityReconciliation(t *testing.T) {
	f := &fakeMCP{resources: []client.MCPResource{
		{Server: "a", URI: "b:c", Name: "first"}, {Server: "a:b", URI: "c", Name: "second"},
		{Server: "a", URI: "b:c", Name: "duplicate must not activate"}, {Server: "a\t", URI: "b:c", Name: "third"},
	}}
	m := openOverlay(t, newMCPModel(t, aztec(), f), ctrlKey('r'))
	s := mcpActive(m)
	if len(s.resources) != 3 {
		t.Fatal("exact identities not deterministically deduplicated")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	id := s.resList.CursorID()
	f.resources = []client.MCPResource{f.resources[3], f.resources[1], f.resources[0]}
	load := s.pickerRequest("list resources", func(ctx context.Context) tea.Cmd { return client.ListMcpResourcesCmd(ctx, s.mcp, "") })
	m = applyAll(m, runCmd(load))
	if s.resList.CursorID() != id || s.resources[s.resList.Cursor()].Server != "a:b" {
		t.Fatal("reorder lost raw identity")
	}
	f.resources = f.resources[:1]
	load = s.pickerRequest("list resources", func(ctx context.Context) tea.Cmd { return client.ListMcpResourcesCmd(ctx, s.mcp, "") })
	m = applyAll(m, runCmd(load))
	if s.resList.Cursor() != 0 || s.resList.CursorID() == id {
		t.Fatal("removed selection fallback invalid")
	}
	_ = m
}

func TestMCPPreviewRoutedFullPayloadPositionAndBack(t *testing.T) {
	f := browserInventory()
	raw := "start\t\x1b[31m\n" + strings.Repeat("loaded line\n", maxToolResultLines+80) + "beyond-old-cutoff"
	f.contents = []client.MCPResourceContents{{Text: raw}}
	m := openOverlay(t, newMCPModel(t, aztec(), f), ctrlKey('r'))
	s := mcpActive(m)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	for range 5 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	_ = m.View()
	saved, selected := s.resList.Offset(), s.resList.CursorID()
	m, cmd := pickerKey(m, tea.KeyEnter)
	m = applyAll(m, runCmd(cmd))
	if s.previewResource.URI != "raw:1" {
		t.Fatal("preview did not retain originating resource identity")
	}
	_ = m.View()
	bounds := *m.metrics
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	body := stripANSIstr(m.renderBody())
	if !strings.Contains(body, "beyond-old-cutoff") || !strings.Contains(body, "insert into prompt") || !strings.Contains(body, "back") {
		t.Fatalf("preview body/actions missing: %s", body)
	}
	if strings.Contains(body, "\t") || strings.Contains(body, "\x1b") {
		t.Fatal("unsafe preview")
	}
	_ = m.View()
	if *m.metrics != bounds {
		t.Fatal("preview scroll resized card")
	}
	position := s.previewViewport.Offset()
	m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 0}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if s.previewViewport.Offset() != position || m.modal == nil {
		t.Fatal("compact preview changed reading position or inserted")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	_ = m.View()
	if s.previewViewport.Offset() != position {
		t.Fatal("restoration reset preview")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 0}, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != s || s.view != mcpResources || s.resList.CursorID() != selected || s.resList.Offset() != saved {
		t.Fatal("compact Back did not restore inventory window")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, cmd = pickerKey(m, tea.KeyEnter)
	m = applyAll(m, runCmd(cmd))
	_ = m.View()
	if s.previewViewport.Offset() != 0 {
		t.Fatal("fresh read did not reset reading position")
	}
	// The surface effect must retain the raw payload even though the existing
	// textarea Rewrite policy normalizes control bytes and tabs when displaying a draft.
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	intent := s.takeSurfaceIntent().(promptInsertIntent)
	if intent.text != raw {
		t.Fatal("preview display transformations changed the insertion payload")
	}
	expected := m.prompt
	expected.Rewrite(raw)
	m, _ = pickerKey(m, tea.KeyEnter)
	if m.modal != nil || m.prompt.Value() != expected.Value() {
		t.Fatalf("root insertion did not retain existing Rewrite semantics: %q", m.prompt.Value())
	}
}

type browserMCP struct {
	*fakeMCP
	server, name string
}

func (f *browserMCP) GetMCPPrompt(ctx context.Context, server, name string, args map[string]string) (string, []client.MCPPromptMessage, error) {
	f.server, f.name = server, name
	return f.fakeMCP.GetMCPPrompt(ctx, server, name, args)
}

func (f *browserMCP) ReadMCPResource(ctx context.Context, server, uri string) ([]client.MCPResourceContents, error) {
	f.server, f.name = server, uri
	return f.fakeMCP.ReadMCPResource(ctx, server, uri)
}

func TestMCPBrowserPromptIdentityAndFormBackWindow(t *testing.T) {
	f := &browserMCP{fakeMCP: browserInventory()}
	// These pairs would collide with delimiter concatenation; tab/control-bearing
	// pairs must also remain distinct even when their display labels coincide.
	f.prompts[0] = client.MCPPrompt{Server: "a", Name: "b:c"}
	f.prompts[1] = client.MCPPrompt{Server: "a:b", Name: "c"}
	f.prompts[2] = client.MCPPrompt{Server: "a\t", Name: "b:c"}
	f.prompts[3] = client.MCPPrompt{Server: "a\x1b[31m", Name: "b:c"}
	f.prompts = append(f.prompts, client.MCPPrompt{Server: "a:b", Name: "c", Arguments: []client.MCPPromptArgument{{Name: "duplicate", Required: true}}})
	m := openOverlay(t, newMCPModel(t, aztec(), f), tea.KeyPressMsg{Code: tea.KeyF8})
	s := mcpActive(m)
	if len(s.prompts) != 60 {
		t.Fatal("prompt duplicates were not deterministically reconciled")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	selected := s.prList.CursorID()
	f.prompts[0], f.prompts[1] = f.prompts[1], f.prompts[0]
	load := s.pickerRequest("list prompts", func(ctx context.Context) tea.Cmd { return client.ListMcpPromptsCmd(ctx, s.mcp, "") })
	m = applyAll(m, runCmd(load))
	if s.prList.CursorID() != selected || s.prList.Cursor() != 0 {
		t.Fatal("prompt reconciliation lost raw identity")
	}
	m, cmd := pickerKey(m, tea.KeyEnter)
	m = applyAll(m, runCmd(cmd))
	if f.server != "a:b" || f.name != "c" || m.modal != nil {
		t.Fatal("prompt activated a different duplicate/payload")
	}

	f.fakeMCP = browserInventory()
	f.prompts[0].Arguments = []client.MCPPromptArgument{{Name: "required", Required: true}}
	m = openOverlay(t, m, tea.KeyPressMsg{Code: tea.KeyF8})
	s = mcpActive(m)
	for range 5 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	_ = m.View()
	window, identity := s.prList.Offset(), s.prList.CursorID()
	m, _ = pickerKey(m, tea.KeyEnter)
	m, _ = pickerKey(m, tea.KeyEsc)
	_ = m.View()
	if s.view != mcpPrompts || s.prList.Offset() != window || s.prList.CursorID() != identity {
		t.Fatal("form Back reset prompt inventory window")
	}
}

func TestMCPPreviewRoutedGeometryAndWheel(t *testing.T) {
	f := &browserMCP{fakeMCP: sampleResourceMCP()}
	f.resources[0] = client.MCPResource{Server: "raw\tserver\x1b[31m", URI: "raw:uri\t\x00", Name: "preview"}
	f.contents = []client.MCPResourceContents{{Text: strings.Repeat("wide\tvalue\x1b[31m 界\n", 100)}}
	m := newMCPModel(t, aztec(), f)
	m.keys.ScrollD = key.NewBinding(key.WithKeys("f"))
	m.keys.ScrollBottom = key.NewBinding(key.WithKeys("G"))
	m.keys.ScrollTop = key.NewBinding(key.WithKeys("g"))
	m = openOverlay(t, m, ctrlKey('r'))
	m, cmd := pickerKey(m, tea.KeyEnter)
	m = applyAll(m, runCmd(cmd))
	if f.server != f.resources[0].Server || f.name != f.resources[0].URI {
		t.Fatal("resource RPC arguments were display-sanitized")
	}
	s := mcpActive(m)
	m = applyAll(m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if s.previewViewport.Offset() == 0 {
		t.Fatal("remapped page did not scroll preview")
	}
	before := s.previewViewport.Offset()
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.previewViewport.Offset() <= before {
		t.Fatal("preview wheel did not browse")
	}
	before = s.previewViewport.Offset()
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 36})
	_ = m.View()
	if s.previewViewport.Offset() != before {
		t.Fatal("noncompact resize reset physical reading position")
	}
	for _, size := range [][2]int{{120, 36}, {24, 18}, {80, 8}, {1, 1}, {0, 0}, {-1, -1}} {
		m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.View()
		before = s.previewViewport.Offset()
		m = applyAll(m, tea.KeyPressMsg{Code: 'G', Text: "G"})
		_ = m.View()
		if s.compact && s.previewViewport.Offset() != before {
			t.Fatal("compact navigation moved reading position")
		}
		if m.width <= 0 || m.vp.Height() <= 0 {
			if !s.compact {
				t.Fatalf("%v: unusable root offer did not compact", size)
			}
			continue
		}
		b := m.metrics.outerBounds
		if b.x0 < 0 || b.x1 > m.width || b.y0 < convTopRow(m) || b.y1 > convTopRow(m)+m.vp.Height() {
			t.Fatalf("%v: root-rendered preview bounds %+v outside conversation region", size, b)
		}
		if !s.compact && b.x1-b.x0 > 128 {
			t.Fatalf("%v: framed preview exceeds width cap: %+v", size, b)
		}
		raw := m.renderBody()
		if strings.Contains(raw, "\x1b[31m") {
			t.Fatal("untrusted preview escape survived styling")
		}
		plain := stripANSIstr(raw)
		if strings.ContainsAny(plain, "\t\x1b\x00") {
			t.Fatal("unsafe preview chrome/body")
		}
		if !s.compact && (!strings.Contains(plain, "insert into prompt") || !strings.Contains(plain, "back")) {
			t.Fatal("preview controls are not pinned")
		}
	}
}

func TestMCPPreviewCapturesReadIdentity(t *testing.T) {
	m := openOverlay(t, newMCPModel(t, aztec(), sampleResourceMCP()), ctrlKey('r'))
	m, cmd := pickerKey(m, tea.KeyEnter)
	s := mcpActive(m)
	s.resList.SetCursor(1)
	m = applyAll(m, runCmd(cmd))
	if s.previewResource.URI != "https://example.com/a" || !strings.Contains(stripANSIstr(m.renderBody()), "page-a") {
		t.Fatal("preview identity followed the mutable list cursor")
	}
}

func TestMCPPreviewEmptyAndBinaryInsertion(t *testing.T) {
	for _, contents := range [][]client.MCPResourceContents{nil, {{Text: ""}}, {{Blob: []byte{1, 2, 3}, MimeType: "application/octet-stream"}}} {
		f := sampleResourceMCP()
		f.contents = contents
		m := openOverlay(t, newMCPModel(t, aztec(), f), ctrlKey('r'))
		m, cmd := pickerKey(m, tea.KeyEnter)
		m = applyAll(m, runCmd(cmd))
		m.prompt.Rewrite("replace this")
		m, _ = pickerKey(m, tea.KeyEnter)
		if m.modal != nil || m.prompt.Value() != joinContents(contents) {
			t.Fatal("empty/binary resource was not inserted")
		}
	}
}
