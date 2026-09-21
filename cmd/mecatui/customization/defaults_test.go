package customization

import (
	"strings"
	"testing"

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
