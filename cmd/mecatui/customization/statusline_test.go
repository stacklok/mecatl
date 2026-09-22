// Package customization tests the dependency-leaf presentation customization protocol.
package customization

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

func TestStatusCustomization_Scenario1_StatusInputExcludesSensitiveContent(t *testing.T) {
	typeOfInput := reflect.TypeFor[Input]()
	allowed := map[string]struct{}{
		"Version":    {},
		"Server":     {},
		"Session":    {},
		"Model":      {},
		"Usage":      {},
		"Context":    {},
		"Workspace":  {},
		"Terminal":   {},
		"MainAgent":  {},
		"Delegation": {},
		"Clock":      {},
	}
	for i := range typeOfInput.NumField() {
		name := typeOfInput.Field(i).Name
		if _, ok := allowed[name]; !ok {
			t.Fatalf("StatusInput exposes non-allowlisted field %q", name)
		}
	}
	if got, want := typeOfInput.NumField(), len(allowed); got != want {
		t.Fatalf("StatusInput field count = %d, want %d", got, want)
	}

	for _, forbidden := range []string{
		"prompt", "transcript", "tool", "credential", "secret", "token",
		"password", "authentication", "authorization", "diagnostic", "launch",
	} {
		if strings.Contains(strings.ToLower(typeOfInput.String()), forbidden) {
			t.Fatalf("StatusInput type leaks %q", forbidden)
		}
	}

	server := reflect.TypeFor[ServerTarget]()
	if got, want := server.NumField(), 2; got != want {
		t.Fatalf("ServerTarget field count = %d, want %d", got, want)
	}
	for i := range server.NumField() {
		name := strings.ToLower(server.Field(i).Name)
		if strings.Contains(name, "auth") || strings.Contains(name, "token") || strings.Contains(name, "credential") {
			t.Fatalf("ServerTarget exposes authentication data via %q", server.Field(i).Name)
		}
	}

	workspace := reflect.TypeFor[Workspace]()
	if got, want := workspace.NumField(), 3; got != want {
		t.Fatalf("Workspace field count = %d, want %d", got, want)
	}
	for i := range workspace.NumField() {
		if name := workspace.Field(i).Name; name == "Launch" || name == "Basename" {
			t.Fatalf("workspace must not expose %q", name)
		}
	}
}

func TestStatusLine_ConnectionModeUsesEmbeddedOrConnect(t *testing.T) {
	server := reflect.TypeFor[ServerTarget]()
	field, ok := server.FieldByName("ConnectionMode")
	if !ok || field.Type.Kind() != reflect.String {
		t.Fatalf("ConnectionMode = %#v, want string field", field)
	}
	if _, ok := server.FieldByName("Transport"); ok {
		t.Fatal("status-facing server field must be named ConnectionMode, not Transport")
	}
	for _, mode := range []string{"embedded", "connect"} {
		input := Input{Server: ServerTarget{ConnectionMode: mode}}
		if input.Server.ConnectionMode != mode {
			t.Fatalf("connection mode = %q, want %q", input.Server.ConnectionMode, mode)
		}
	}
}

func TestStatusLine_InputCarriesUsageContextAndSurfaceColumns(t *testing.T) {
	input := Input{
		Model: Model{
			ProviderID:    "openai",
			ID:            "gpt-5",
			DisplayName:   "GPT-5",
			ContextWindow: ContextAtom{Raw: 100, Human: "100"},
		},
		Session: Session{Title: "Status work", ReasoningEffort: "high"},
		Usage: Usage{
			Input:            UsageAtom{Raw: 100, Human: "100"},
			Output:           UsageAtom{Raw: 25, Human: "25"},
			CacheRead:        UsageAtom{Raw: 50, Human: "50"},
			CacheWrite:       UsageAtom{Raw: 10, Human: "10"},
			CacheReadPercent: 50,
		},
		Context: Context{
			Used:    ContextAtom{Raw: 75, Human: "75"},
			Window:  ContextAtom{Raw: 100, Human: "100"},
			Percent: 75,
		},
		Terminal:  Terminal{Rows: 40, Cols: 120, HeaderAvailCols: 92, FooterAvailCols: 101},
		MainAgent: MainAgent{State: "running", Activity: "thinking", Approval: "awaiting"},
		Delegation: Delegation{
			DirectSubagent: DelegationStateCounts{Running: 1, AwaitingApproval: 1, Completed: 2, Failed: 1, Cancelled: 1, Stopped: 1},
			TeamMember:     DelegationStateCounts{Running: 2, Completed: 1, Stopped: 1},
			ParallelBranch: DelegationStateCounts{AwaitingApproval: 1, Failed: 1},
			Total:          DelegationStateCounts{Running: 3, AwaitingApproval: 2, Completed: 3, Failed: 2, Cancelled: 1, Stopped: 2},
		},
	}
	if got, want := input.Usage.CacheReadPercent, 50; got != want {
		t.Fatalf("cache read percent = %d, want %d", got, want)
	}
	if got, want := input.Context.Window.Human, "100"; got != want {
		t.Fatalf("context window human = %q, want %q", got, want)
	}
	if got, want := input.Terminal.HeaderAvailCols, 92; got != want {
		t.Fatalf("header available columns = %d, want %d", got, want)
	}
	if got, want := input.Terminal.FooterAvailCols, 101; got != want {
		t.Fatalf("footer available columns = %d, want %d", got, want)
	}
	if got, want := input.Model.ProviderID, "openai"; got != want {
		t.Fatalf("model provider ID = %q, want %q", got, want)
	}
	if got, want := input.Model.ID, "gpt-5"; got != want {
		t.Fatalf("model ID = %q, want %q", got, want)
	}
	if got, want := input.Model.DisplayName, "GPT-5"; got != want {
		t.Fatalf("model display name = %q, want %q", got, want)
	}
	if got, want := input.Model.ContextWindow.Human, "100"; got != want {
		t.Fatalf("model context window human = %q, want %q", got, want)
	}
	if got, want := input.Session.ReasoningEffort, "high"; got != want {
		t.Fatalf("reasoning effort = %q, want %q", got, want)
	}
	if !input.Delegation.Valid() {
		t.Fatal("delegation totals do not sum their leaf state counts")
	}
	if got, want := input.Delegation.Total.AwaitingApproval, 2; got != want {
		t.Fatalf("delegation awaiting approval = %d, want %d", got, want)
	}
	input.Delegation.Total.Failed++
	if input.Delegation.Valid() {
		t.Fatal("delegation with mismatched totals is valid")
	}
}

func TestStatusLine_ShippedFooterNormalTextUsesTextToken(t *testing.T) {
	source := NewDefaultSource(0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{
		Usage:    Usage{Input: UsageAtom{Human: "4K"}, Output: UsageAtom{Human: "1K"}},
		Context:  Context{Used: ContextAtom{Human: "2K"}, Window: ContextAtom{Raw: 10_000, Human: "10K"}, Percent: 20},
		Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80},
	})
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("shipped source did not publish")
	}
	for _, span := range source.Latest().Footer.Spans {
		if strings.Contains(span.Text, "cache") && span.Token != TokenText {
			t.Fatalf("footer usage token = %q, want %q", span.Token, TokenText)
		}
	}
}

func TestStatusLine_Scenario2_StatusMLUsesThemeAndSanitizesTerminal(t *testing.T) {
	palette := testPalette{
		TokenText: "custom-text", TokenMuted: "custom-muted", TokenPrimary: "custom-primary",
		TokenSecondary: "custom-secondary", TokenAccent: "custom-accent", TokenSuccess: "custom-success",
		TokenWarning: "custom-warning", TokenError: "custom-error", TokenInfo: "custom-info",
	}
	doc := Render(`<header><text>text</text><muted>muted</muted><primary>primary</primary><secondary>secondary</secondary><accent>accent</accent><success>success</success><warning>warning</warning><error>error`+"\x1b]8;;https://bad.example\x07"+`</error><info>info</info><muted>idle</muted></header><footer><success>ready</success></footer>`, palette)

	wantTokens := []Token{TokenText, TokenMuted, TokenPrimary, TokenSecondary, TokenAccent, TokenSuccess, TokenWarning, TokenError, TokenInfo, TokenMuted}
	if got, want := len(doc.Header.Spans), len(wantTokens); got != want {
		t.Fatalf("header semantic span count = %d, want %d", got, want)
	}
	for i, span := range doc.Header.Spans {
		if got, want := span.Token, wantTokens[i]; got != want {
			t.Fatalf("header token %d = %q, want %q", i, got, want)
		}
		if got, want := span.Color, palette.StatusColor(span.Token); got != want {
			t.Fatalf("%s color = %q, want custom active-palette color %q", span.Token, got, want)
		}
	}
	for _, surface := range []Surface{doc.Header, doc.Footer} {
		for _, span := range surface.Spans {
			if got := span.Text + span.Color; got != terminaltext.SanitizeSingleLine(got) {
				t.Fatalf("terminal control reached output span %#v", span)
			}
		}
	}

	for _, control := range []string{"\n", "\t", "\u2028"} {
		doc := Render(`<footer><text>before`+control+`after</text></footer>`, palette)
		if got := doc.Footer.Spans[0].Text; got != "beforeafter" {
			t.Fatalf("StatusML control %q rendered as %q, want a single-line value", control, got)
		}
	}

	unknown := Render(`<footer><unknown>unsafe</unknown></footer>`, palette)
	if got := unknown.Footer.Spans[0].Text; got != `<footer><unknown>unsafe</unknown></footer>` {
		t.Fatalf("unknown markup = %q, want safely literalized source", got)
	}
	malformed := Render(`<footer><primary>unsafe</footer>`, palette)
	if got := malformed.Footer.Spans[0].Text; got != `<footer><primary>unsafe</footer>` {
		t.Fatalf("malformed markup = %q, want safely literalized source", got)
	}
	legacy := Render(`<footer><right><text>legacy</text></right></footer>`, palette)
	if got := legacy.Footer.Spans[0].Text; got != `<footer><right><text>legacy</text></right></footer>` {
		t.Fatalf("legacy layout markup = %q, want safely literalized source", got)
	}
}

func TestStatusLine_LinkNodeValidatesAndRendersThemeStyle(t *testing.T) {
	palette := testPalette{TokenText: "text", TokenAccent: "accent", LinkColor: "link", LinkUnderline: true}
	doc := Render(`<footer><link href="https://example.test/path">documentation</link></footer>`, palette)
	span := doc.Footer.Spans[0]
	if got, want := span.Text, "documentation"; got != want {
		t.Fatalf("link display = %q, want %q", got, want)
	}
	if got, want := span.Href, "https://example.test/path"; got != want {
		t.Fatalf("link href = %q, want %q", got, want)
	}
	if got, want := span.Color, "link"; got != want {
		t.Fatalf("link color = %q, want %q", got, want)
	}
	if !span.Underline {
		t.Fatal("link is not underlined by the active theme")
	}

	invalid := Render(`<footer><link href="javascript:alert(1)">safe`+"\x1b"+` text</link></footer>`, palette)
	if got, want := invalid.Footer.Spans[0].Text, "safe text"; got != want {
		t.Fatalf("invalid link display = %q, want safely preserved text %q", got, want)
	}
	if got := invalid.Footer.Spans[0].Href; got != "" {
		t.Fatalf("invalid link retained href %q", got)
	}
}

type testPalette struct {
	TokenText, TokenMuted, TokenPrimary, TokenSecondary, TokenAccent string
	TokenSuccess, TokenWarning, TokenError, TokenInfo                string
	LinkColor                                                        string
	LinkUnderline                                                    bool
}

func (p testPalette) StatusColor(token Token) string {
	return map[Token]string{
		TokenText: p.TokenText, TokenMuted: p.TokenMuted, TokenPrimary: p.TokenPrimary,
		TokenSecondary: p.TokenSecondary, TokenAccent: p.TokenAccent, TokenSuccess: p.TokenSuccess,
		TokenWarning: p.TokenWarning, TokenError: p.TokenError, TokenInfo: p.TokenInfo,
	}[token]
}

func (p testPalette) StatusLinkColor() string   { return p.LinkColor }
func (p testPalette) StatusLinkUnderline() bool { return p.LinkUnderline }
