package ui

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	statusline "github.com/stacklok/mecatl/cmd/mecatui/statusline"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestADR_0247_UIStoresOnlyResult(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "model.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	modelFields := structFields(t, file, "Model")
	depsFields := structFields(t, file, "Deps")
	for _, forbidden := range []string{"StatusLine", "statusLine", "activeStatusLine", "StatusSurface"} {
		for _, field := range append(modelFields, depsFields...) {
			if field == forbidden {
				t.Fatalf("Model retains legacy status-line field %q", field)
			}
		}
	}
	if !containsField(modelFields, "generatedStatusLine") {
		t.Fatalf("Model fields %v omit generated status line", modelFields)
	}
}

func structFields(t *testing.T, file *ast.File, structName string) []string {
	t.Helper()
	var fields []string
	ast.Inspect(file, func(node ast.Node) bool {
		typeSpec, ok := node.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != structName {
			return true
		}
		structType, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			t.Fatalf("%s is not a struct", structName)
		}
		for _, field := range structType.Fields.List {
			for _, name := range field.Names {
				fields = append(fields, name.Name)
			}
		}
		return false
	})
	if fields == nil {
		t.Fatalf("missing %s fields", structName)
	}
	return fields
}

func containsField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

func TestADR_0247_SourceAdapterRearmsAndInstallsLatest(t *testing.T) {
	s := &statusSourceFake{changed: make(chan struct{}, 1)}
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: s})
	s.publish(statusline.Result{Header: statusline.Surface{Present: true, Spans: []statusline.Span{{Text: "first"}}}})
	updated, rearm := m.update(waitStatusMessage(t, m.statusLineWaitCmd()))
	m = updated.(Model)
	if m.generatedStatusLine.Header.Spans[0].Text != "first" {
		t.Fatal("not installed")
	}
	s.publish(statusline.Result{Footer: statusline.Surface{Present: true, Spans: []statusline.Span{{Text: "second"}}}})
	updated, _ = m.update(waitStatusMessage(t, rearm))
	if updated.(Model).generatedStatusLine.Footer.Spans[0].Text != "second" {
		t.Fatal("not rearmed")
	}
}
func TestStatusCustomization_Scenario1_StatusInputProjectsLiveUIState(t *testing.T) {
	s := &statusSourceFake{changed: make(chan struct{})}
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: s, Server: "server.example", ConnectionMode: "embedded"})
	m.phase = phaseRunning
	m.sessionTitle = "Status work"
	m.resolvedSessionModel.ProviderID = "openai"
	m.resolvedSessionModel.ModelID = "gpt-5"
	m.resolvedSessionModel.ReasoningEffort = "high"
	m.resolvedSessionModel.ContextWindow = 200_000
	m.usage.InputTokens = 12_300
	m.usage.OutputTokens = 456
	m.usage.CacheReadTokens = 9_840
	m.usage.CacheWriteTokens = 1_200
	m.contextTokens = 45_600
	m.activePlacement = client.Placement{Kind: "local", Label: "status-work"}
	m.activeTool = "Read"
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	input, ok := s.lastInput()
	if !ok {
		t.Fatal("missing input")
	}
	if input.Version != statusline.ProtocolVersion || input.Server != (statusline.ServerTarget{DisplayTarget: "server.example", ConnectionMode: "embedded"}) {
		t.Fatalf("server/version projection = %#v", input)
	}
	if input.Session.Title != "Status work" || input.Session.ReasoningEffort != "high" || input.Session.Mode != "default" || input.Session.Handle != "" {
		t.Fatalf("session projection = %#v", input.Session)
	}
	if input.Model.ProviderID != "openai" || input.Model.ID != "gpt-5" || input.Model.DisplayName != "gpt-5" || input.Model.ContextWindow != (statusline.ContextAtom{Raw: 200_000, Human: "200K"}) {
		t.Fatalf("model projection = %#v", input.Model)
	}
	if input.Usage != (statusline.Usage{
		Input:            statusline.UsageAtom{Raw: 12_300, Human: "12.3K"},
		Output:           statusline.UsageAtom{Raw: 456, Human: "456"},
		CacheRead:        statusline.UsageAtom{Raw: 9_840, Human: "9.8K"},
		CacheWrite:       statusline.UsageAtom{Raw: 1_200, Human: "1.2K"},
		CacheReadPercent: 80,
	}) {
		t.Fatalf("usage projection = %#v", input.Usage)
	}
	if input.Context != (statusline.Context{Used: statusline.ContextAtom{Raw: 45_600, Human: "45.6K"}, Window: statusline.ContextAtom{Raw: 200_000, Human: "200K"}, Percent: 22}) {
		t.Fatalf("context projection = %#v", input.Context)
	}
	if input.Workspace != (statusline.Workspace{Location: "local", Basename: "status-work"}) {
		t.Fatalf("workspace projection = %#v", input.Workspace)
	}
	if input.MainAgent != (statusline.MainAgent{State: "running_tool", Activity: "Read", Approval: "none"}) || !input.Delegation.Valid() {
		t.Fatalf("agent/delegation projection = %#v / %#v", input.MainAgent, input.Delegation)
	}
	if input.Terminal.Rows != 40 || input.Terminal.Cols != 120 || input.Terminal.HeaderAvailCols <= 0 || input.Terminal.FooterAvailCols <= 0 {
		t.Fatalf("terminal projection = %#v", input.Terminal)
	}
	if input.Clock.Now.IsZero() {
		t.Fatal("clock projection is zero")
	}
}

func TestStatusCustomization_Scenario1_StatusInputExcludesRemoteWorkspacePath(t *testing.T) {
	s := &statusSourceFake{changed: make(chan struct{})}
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: s, ConnectionMode: "connect"})
	m.activePlacement = client.Placement{Kind: "remote", Label: "safe-label"}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	input, ok := s.lastInput()
	if !ok {
		t.Fatal("missing input")
	}
	if input.Workspace.Location != "remote" || input.Workspace.Path != "" || input.Workspace.Basename != "safe-label" {
		t.Fatalf("remote workspace leaked as local command path: %#v", input.Workspace)
	}
}

func TestStatusCustomization_Scenario2_ReservedLanesAndResponsiveSelection(t *testing.T) {
	s := &statusSourceFake{changed: make(chan struct{})}
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: s})
	m.phase = phaseRunning
	m.activeTool = ""

	// Rendering is pure: it must not create source work or update status input.
	m.renderFooter()
	if got := s.inputCount(); got != 0 {
		t.Fatalf("render submitted %d status inputs, want 0", got)
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	input, ok := s.lastInput()
	if !ok {
		t.Fatal("resize did not submit status input")
	}
	if input.MainAgent.State != "thinking" || input.MainAgent.Activity != "" {
		t.Fatalf("thinking input = %#v", input.MainAgent)
	}
	geometry := m.statusLineGeometry()
	if input.Terminal.HeaderAvailCols != geometry.headerAvailable || input.Terminal.FooterAvailCols != geometry.footerAvailable {
		t.Fatalf("reserved-lane widths = %#v, want %#v", input.Terminal, geometry)
	}
	if want := m.widthOr() - 2 - lipgloss.Width(m.footerActivity()) - footerGapPad; input.Terminal.FooterAvailCols != want {
		t.Fatalf("footer available columns = %d, want %d after footer padding and activity reservation", input.Terminal.FooterAvailCols, want)
	}

	updated, _ = m.Update(client.ToolCallMsg{Name: "Read"})
	m = updated.(Model)
	input, _ = s.lastInput()
	if input.MainAgent.State != "running_tool" || input.MainAgent.Activity != "Read" {
		t.Fatalf("tool input = %#v", input.MainAgent)
	}
	updated, _ = m.Update(client.ToolProgressMsg{Text: "scanning"})
	m = updated.(Model)
	input, _ = s.lastInput()
	if input.MainAgent.State != "running_tool" || input.MainAgent.Activity != "scanning" {
		t.Fatalf("progress input = %#v", input.MainAgent)
	}
}
func TestADR_0247_SourceResizeSubmitsCanonicalInput(t *testing.T) {
	s := &statusSourceFake{changed: make(chan struct{})}
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: s, Server: "target", ConnectionMode: "embedded"})
	m.phase = phaseIdle
	m.resolvedSessionModel.ContextWindow = 100
	m.usage.InputTokens = 10
	m.contextTokens = 20
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	input, ok := s.lastInput()
	if !ok {
		t.Fatal("missing input")
	}
	if input.Server.ConnectionMode != "embedded" || input.Usage.Input.Raw != 10 || input.Context.Used.Raw != 20 || input.Terminal.HeaderAvailCols <= 0 || input.Terminal.FooterAvailCols <= 0 {
		t.Fatalf("incomplete input %#v", input)
	}
}
func waitStatusMessage(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		return msg
	case <-time.After(time.Second):
		t.Fatal("listener did not wake")
		return nil
	}
}

type statusSourceFake struct {
	changed chan struct{}
	mu      sync.Mutex
	latest  statusline.Result
	inputs  []statusline.Input
}

func (g *statusSourceFake) Submit(input statusline.Input) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inputs = append(g.inputs, input)
}
func (g *statusSourceFake) Changed() <-chan struct{} { return g.changed }
func (g *statusSourceFake) Latest() statusline.Result {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.latest
}
func (*statusSourceFake) Close(context.Context) error { return nil }
func (g *statusSourceFake) publish(line statusline.Result) {
	g.mu.Lock()
	g.latest = line
	g.mu.Unlock()
	g.changed <- struct{}{}
}
func (g *statusSourceFake) lastInput() (statusline.Input, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.inputs) == 0 {
		return statusline.Input{}, false
	}
	return g.inputs[len(g.inputs)-1], true
}

func (g *statusSourceFake) inputCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.inputs)
}
