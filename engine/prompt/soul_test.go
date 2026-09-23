package prompt_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeSoulSource is a scripted prompt.SoulSource for offline tests.
type fakeSoulSource struct {
	body string
	err  error
}

func (f fakeSoulSource) Load(context.Context) (string, error) { return f.body, f.err }

func TestSoulAssemblerRendersUserMessage(t *testing.T) {
	const body = "You are a terse, dry-witted engineer who prefers Go and hates ceremony."
	got, err := prompt.SoulAssembler{Src: fakeSoulSource{body: body}}.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want exactly 1", len(got))
	}
	msg := got[0]
	if msg.Role != session.RoleUser {
		t.Errorf("role = %q, want user (the soul must never ride the system role)", msg.Role)
	}
	if !strings.Contains(msg.Text, body) {
		t.Errorf("rendered soul missing the body:\n%s", msg.Text)
	}
	// The body is fenced as DATA in matching <soul>...</soul> delimiters on their
	// OWN lines, and the body sits BETWEEN them (a prompt-injection guard).
	open := strings.Index(msg.Text, "\n<soul>\n")
	closeIdx := strings.Index(msg.Text, "\n</soul>")
	if open < 0 || closeIdx < 0 || open >= closeIdx {
		t.Fatalf("soul body not wrapped in matching <soul>...</soul> fence:\n%s", msg.Text)
	}
	if i := strings.Index(msg.Text, body); i < open || i > closeIdx {
		t.Errorf("body escaped the data fence:\n%s", msg.Text)
	}
	// The header (everything before the open fence) must tell the model to treat
	// the fenced block as DATA describing its persona, not a new instruction stream.
	if !strings.Contains(msg.Text[:open], "DATA") {
		t.Errorf("header should tell the model to treat the fenced block as data:\n%s", msg.Text)
	}
}

func TestSoulAssemblerFailSoft(t *testing.T) {
	cases := []struct {
		name string
		src  prompt.SoulSource
	}{
		{"nil source", nil},
		{"load error", fakeSoulSource{err: errors.New("disk on fire")}},
		{"empty body", fakeSoulSource{body: ""}},
		{"whitespace-only body", fakeSoulSource{body: "   \n\t  "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := prompt.SoulAssembler{Src: tc.src}.Assemble(context.Background())
			if err != nil {
				t.Errorf("soul faults must fail soft (persona is best-effort), got err=%v", err)
			}
			if got != nil {
				t.Errorf("want no message, got %v", got)
			}
		})
	}
}

// TestSoulNotInStablePrefix is the cache-invariant proof: the soul is turn-0
// conversation content, never part of prompt.Build's StablePrefix.
func TestSoulNotInStablePrefix(t *testing.T) {
	cfg := prompt.Config{Role: "You are a test harness."}
	base := prompt.Build(cfg).StablePrefix

	soulMsg, _ := prompt.SoulAssembler{Src: fakeSoulSource{body: "secret-persona-marker"}}.Assemble(context.Background())
	if len(soulMsg) != 1 {
		t.Fatalf("want a soul message to test against")
	}
	if strings.Contains(base, "secret-persona-marker") {
		t.Errorf("soul content leaked into StablePrefix (cache invariant broken):\n%s", base)
	}
	if again := prompt.Build(cfg).StablePrefix; again != base {
		t.Errorf("StablePrefix not byte-stable across builds")
	}
}

// TestMultiAssemblerSoulBeforeMemory proves the issue #14 ordering: with both a
// soul and a memory index wired alongside the root, the turn-0 messages come out
// root → soul → memory (identity before saved facts).
func TestMultiAssemblerSoulBeforeMemory(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(context.Background(), "AGENTS.md", []byte("be terse")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	multi := prompt.NewMultiAssembler(
		prompt.RootAssembler{Source: ws},
		prompt.SoulAssembler{Src: fakeSoulSource{body: "persona-body"}},
		prompt.MemoryIndexAssembler{Src: fakeIndexSource{entries: []tool.MemoryEntry{{Key: "pref/x", Description: "a pref"}}}},
	)
	got, err := multi.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3 (AGENTS.md, soul, memory)", len(got))
	}
	if !strings.Contains(got[0].Text, "be terse") {
		t.Errorf("first message should be AGENTS.md, got %q", got[0].Text)
	}
	if !strings.Contains(got[1].Text, "persona-body") {
		t.Errorf("second message should be the soul, got %q", got[1].Text)
	}
	if !strings.Contains(got[2].Text, "pref/x") {
		t.Errorf("third message should be the memory index, got %q", got[2].Text)
	}
}
