package customization

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestDefaultTitleTemplateStateLabels(t *testing.T) {
	renderer, err := NewTitleRenderer(DefaultTitleTemplate())
	if err != nil {
		t.Fatalf("NewTitleRenderer() error = %v", err)
	}

	for _, tc := range []struct {
		state string
		want  string
	}{
		{"idle", "○ Ready"},
		{"connecting", "◌ Connecting"},
		{"thinking", "✦ Thinking"},
		{"running_tool", "⚙ Working"},
		{"awaiting_approval", "⚠ Approval needed"},
		{"completed", "✓ Complete"},
		{"failed", "✗ Failed"},
		{"cancelled", "■ Cancelled"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			got, err := renderer.Render(Input{
				Session:   Session{Title: "Session title"},
				MainAgent: MainAgent{State: tc.state},
			})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if want := tc.want + " · Session title · mecatui"; got != want {
				t.Fatalf("Render() = %q, want %q", got, want)
			}
		})
	}
}

func TestDefaultTitleTemplateOmitsUnknownStateSeparators(t *testing.T) {
	renderer, err := NewTitleRenderer(DefaultTitleTemplate())
	if err != nil {
		t.Fatalf("NewTitleRenderer() error = %v", err)
	}

	for _, state := range []string{"", "unknown"} {
		t.Run(state, func(t *testing.T) {
			got, err := renderer.Render(Input{
				Session:   Session{Title: "Session title", Handle: "sess-123"},
				MainAgent: MainAgent{State: state},
			})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if want := "Session title · mecatui"; got != want {
				t.Fatalf("Render() = %q, want %q", got, want)
			}
		})
	}
}

func TestDefaultTitleTemplateUntitledActiveSession(t *testing.T) {
	renderer, err := NewTitleRenderer(DefaultTitleTemplate())
	if err != nil {
		t.Fatalf("NewTitleRenderer() error = %v", err)
	}

	got, err := renderer.Render(Input{
		Session:   Session{Handle: "sess-123"},
		MainAgent: MainAgent{State: "thinking"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if want := "✦ Thinking · mecatui"; got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}

	got, err = renderer.Render(Input{})
	if err != nil {
		t.Fatalf("Render() without a session error = %v", err)
	}
	if want := "mecatui"; got != want {
		t.Fatalf("Render() without a session = %q, want %q", got, want)
	}
}

func TestDefaultTitleTemplateElidesWideSessionTitle(t *testing.T) {
	renderer, err := NewTitleRenderer(DefaultTitleTemplate())
	if err != nil {
		t.Fatalf("NewTitleRenderer() error = %v", err)
	}

	got, err := renderer.Render(Input{
		Session:   Session{Title: strings.Repeat("界", 30)},
		MainAgent: MainAgent{State: "thinking"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	const prefix = "✦ Thinking · "
	const suffix = " · mecatui"
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, suffix) {
		t.Fatalf("Render() = %q, want title wrapped by %q and %q", got, prefix, suffix)
	}
	title := strings.TrimSuffix(strings.TrimPrefix(got, prefix), suffix)
	if width := ansi.StringWidth(title); width > 40 {
		t.Fatalf("visible title width = %d, want at most 40: %q", width, title)
	}
	if !strings.HasSuffix(title, "…") {
		t.Fatalf("elided title = %q, want ellipsis", title)
	}
}

func TestCanonicalStatus_Scenario3_UnknownContextDoesNotClaimZeroPressure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		footerCols int
		want       string
		delegation Delegation
		wantTeam   bool
	}{
		{name: "full", footerCols: 120, want: "ctx ?/200K"},
		{name: "compact", footerCols: 20, want: "ctx ?", delegation: Delegation{Team: LiveTeam{Working: 1, Total: 1}}, wantTeam: true},
		{name: "minimal", footerCols: 11, want: "ctx ?", delegation: Delegation{Team: LiveTeam{Working: 1, Total: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			footer := stockFooter(t, Input{
				Context:    Context{Used: ContextAtom{Human: "?"}, Window: ContextAtom{Raw: 200_000, Human: "200K"}},
				Delegation: tc.delegation,
				Terminal:   Terminal{FooterAvailCols: tc.footerCols},
			})
			got := statusSurfaceText(footer)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("footer = %q, want unknown context containing %q", got, tc.want)
			}
			if tc.wantTeam {
				if !strings.Contains(got, "⟳ 1/1") || strings.Contains(got, "working") {
					t.Fatalf("footer = %q, want compact team cue without the full team ratio", got)
				}
			} else if strings.Contains(got, "⟳") {
				t.Fatalf("footer = %q, retained compact delegation content instead of selecting minimal", got)
			}
			if strings.Contains(got, "ctx 0%") {
				t.Fatalf("footer = %q, fabricated zero pressure for unknown context", statusSurfaceText(footer))
			}
			for _, span := range footer.Spans {
				if span.Token == TokenSuccess || span.Token == TokenWarning {
					t.Fatalf("footer = %#v, assigns a pressure token to unknown context", footer)
				}
			}
		})
	}
}

func TestCanonicalStatus_Scenario3_EstimatedContextMarkedAtEveryWidth(t *testing.T) {
	for _, tc := range []struct {
		name       string
		footerCols int
	}{
		{name: "full", footerCols: 120},
		{name: "compact", footerCols: 20},
		{name: "minimal", footerCols: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			estimated := stockFooter(t, Input{
				Context:  Context{Used: ContextAtom{Raw: 75_000, Human: "~75K"}, Window: ContextAtom{Raw: 100_000, Human: "100K"}, Percent: 75, Known: true, Estimated: true},
				Terminal: Terminal{FooterAvailCols: tc.footerCols},
			})
			if text := statusSurfaceText(estimated); !strings.Contains(text, "~75%") {
				t.Fatalf("estimated footer = %q, want visibly estimated percentage", text)
			}

			known := stockFooter(t, Input{
				Context:  Context{Used: ContextAtom{Raw: 90_000, Human: "90K"}, Window: ContextAtom{Raw: 100_000, Human: "100K"}, Percent: 90, Known: true},
				Terminal: Terminal{FooterAvailCols: tc.footerCols},
			})
			if text := statusSurfaceText(known); strings.Contains(text, "~90%") || !strings.Contains(text, "90%") {
				t.Fatalf("known footer = %q, want ordinary percentage", text)
			}
			if tc.name != "minimal" {
				for _, span := range known.Spans {
					if span.Token == TokenWarning {
						return
					}
				}
				t.Fatalf("known footer = %#v, want warning pressure token", known)
			}
		})
	}
}

func stockFooter(t *testing.T, input Input) Surface {
	t.Helper()
	source := NewDefaultSource(0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(input)
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("stock source did not publish")
	}
	return source.Latest().Footer
}

func TestDefaultStatusHeadersElideSessionTitleByVariant(t *testing.T) {
	const title = "界界界界界界界界界界界界界界界界界界界界"
	for _, tc := range []struct {
		name, suffix      string
		width, titleWidth int
	}{
		{name: "full", width: 80, titleWidth: 32, suffix: " · openai/GPT-5"},
		{name: "compact", width: 45, titleWidth: 24, suffix: " · GPT-5"},
		{name: "minimal", width: 24, titleWidth: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := NewDefaultSource(0)
			t.Cleanup(func() { _ = source.Close(context.Background()) })
			source.Submit(Input{
				Session:  Session{Title: title},
				Model:    Model{ProviderID: "openai", DisplayName: "GPT-5"},
				Terminal: Terminal{HeaderAvailCols: tc.width},
			})
			select {
			case <-source.Changed():
			case <-time.After(time.Second):
				t.Fatal("source did not publish")
			}
			header := statusSurfaceText(source.Latest().Header)
			if !strings.HasPrefix(header, "mecatui · ") || !strings.HasSuffix(header, tc.suffix) {
				t.Fatalf("header = %q, want title between mecatui and %q", header, tc.suffix)
			}
			title := strings.TrimSuffix(strings.TrimPrefix(header, "mecatui · "), tc.suffix)
			if width := ansi.StringWidth(title); width > tc.titleWidth {
				t.Fatalf("title width = %d, want at most %d: %q", width, tc.titleWidth, title)
			}
			if !strings.HasSuffix(title, "…") {
				t.Fatalf("title = %q, want ellipsis", title)
			}
			if width := ansi.StringWidth(header); width > tc.width {
				t.Fatalf("header width = %d, want at most %d: %q", width, tc.width, header)
			}
		})
	}
}
