package prompt_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

// fakeRulesSource is a scripted prompt.RulesSource for offline tests.
type fakeRulesSource struct {
	rules []prompt.Rule
	err   error
}

func (f fakeRulesSource) ListRules(context.Context) ([]prompt.Rule, error) {
	return f.rules, f.err
}

func TestRulesAssemblerNilSource(t *testing.T) {
	got, err := prompt.RulesAssembler{Src: nil}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if got != nil {
		t.Fatalf("nil source must produce nil messages (no-op), got %d", len(got))
	}
}

func TestRulesAssemblerErrorSource(t *testing.T) {
	got, err := prompt.RulesAssembler{Src: fakeRulesSource{err: errors.New("disk on fire")}}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble must fail soft on source error, got err=%v", err)
	}
	if got != nil {
		t.Fatalf("error source must produce nil messages, got %d", len(got))
	}
}

func TestRulesAssemblerEmpty(t *testing.T) {
	got, err := prompt.RulesAssembler{Src: fakeRulesSource{}}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if got != nil {
		t.Fatalf("empty rules must produce nil messages, got %d", len(got))
	}
}

func TestRulesAssemblerOneRuleUnconditional(t *testing.T) {
	src := fakeRulesSource{rules: []prompt.Rule{
		{Name: "testing", Body: "always use t.Run for subtests\n", Origin: prompt.RuleOriginProject},
	}}
	got, err := prompt.RulesAssembler{Src: src}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want exactly 1", len(got))
	}
	msg := got[0]
	if msg.Role != session.RoleUser {
		t.Errorf("role = %q, want user (rules must never ride the system role)", msg.Role)
	}
	if !strings.Contains(msg.Text, "always use t.Run for subtests") {
		t.Errorf("rendered rules missing the body:\n%s", msg.Text)
	}
	// The rule must be fenced in <rule name="testing">...</rule>, with the open
	// fence PRECEDING exactly one close fence (a broken double-close would pass a
	// bare Contains check while garbling the model's context).
	open := strings.Index(msg.Text, `<rule name="testing">`)
	closeIdx := strings.Index(msg.Text, `</rule>`)
	if open < 0 || closeIdx < 0 || closeIdx < open {
		t.Errorf("fence structure broken (open=%d close=%d):\n%s", open, closeIdx, msg.Text)
	}
	if strings.Count(msg.Text, `</rule>`) != 1 {
		t.Errorf("expected exactly one </rule>, got %d:\n%s", strings.Count(msg.Text, `</rule>`), msg.Text)
	}
	// Unconditional rule renders "Applies when: (always)".
	if !strings.Contains(msg.Text, "Applies when: (always)") {
		t.Errorf("unconditional rule missing Applies-when clause:\n%s", msg.Text)
	}
	// The header must be the rulesHeader constant.
	if !strings.HasPrefix(msg.Text, prompt.RulesHeader()) {
		t.Errorf("message does not start with rulesHeader:\n%s", msg.Text)
	}
}

func TestRulesAssemblerSanitizesFenceName(t *testing.T) {
	// A rule name comes from a filename stem, which may legally contain
	// characters that would break the <rule name="..."> fence (CWE-74/LLM01
	// hardening): quotes, angle brackets, and literal newlines.
	src := fakeRulesSource{rules: []prompt.Rule{
		{Name: "evil\"<x>\nsafety", Body: "body\n", Origin: prompt.RuleOriginProject},
	}}
	got, err := prompt.RulesAssembler{Src: src}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want exactly 1", len(got))
	}
	text := got[0].Text
	if strings.Contains(text, `name="evil"`) && !strings.Contains(text, `name="evil'x safety"`) {
		t.Errorf("unsanitized name broke the fence:\n%s", text)
	}
	if !strings.Contains(text, `<rule name="evil'x safety">`) {
		t.Errorf("expected sanitized fence name <rule name=\"evil'x safety\">:\n%s", text)
	}
	// No literal newline may survive inside the fence's attribute line.
	fenceLine := text[strings.Index(text, "<rule name="):]
	fenceLine = fenceLine[:strings.Index(fenceLine, "\n")]
	if strings.Count(fenceLine, `"`) != 2 {
		t.Errorf("fence attribute line malformed: %q", fenceLine)
	}
}

func TestRulesAssemblerPathScoped(t *testing.T) {
	src := fakeRulesSource{rules: []prompt.Rule{
		{Name: "api", Body: "use the v2 API\n", Paths: []string{"**/*_test.go", "**/*.go"}, Origin: prompt.RuleOriginProject},
	}}
	got, err := prompt.RulesAssembler{Src: src}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want exactly 1", len(got))
	}
	msg := got[0]
	if !strings.Contains(msg.Text, "**/*_test.go, **/*.go") {
		t.Errorf("path-scoped rule missing globs in Applies-when:\n%s", msg.Text)
	}
	if strings.Contains(msg.Text, "(always)") {
		t.Errorf("path-scoped rule must not say (always):\n%s", msg.Text)
	}
}

func TestRulesAssemblerByteCap(t *testing.T) {
	// Use a fixed small byte cap that fits the header and exactly one rule, but not
	// two. The header length plus one rule block is predictable, so we set MaxBytes
	// to exactly that value.
	rules := []prompt.Rule{
		{Name: "first", Body: "rule one body\n", Origin: prompt.RuleOriginProject},
		{Name: "second", Body: "rule two body\n", Origin: prompt.RuleOriginProject},
	}
	src := fakeRulesSource{rules: rules}

	// Render with a very large cap to see all rules, then measure the header.
	all, err := prompt.RulesAssembler{Src: src, MaxBytes: 100000, MaxCount: 100}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected one message, got %d", len(all))
	}
	fullText := all[0].Text
	if !strings.Contains(fullText, "rule two body") {
		t.Fatalf("both rules must appear with a large cap; got:\n%s", fullText)
	}

	// Compute the byte position right after the first </rule> fence: everything up
	// to and including the first "</rule>". Setting MaxBytes to this length means
	// the second rule's next byte would trip the cap.
	firstCloseIdx := strings.Index(fullText, "</rule>")
	if firstCloseIdx < 0 {
		t.Fatalf("first </rule> not found")
	}
	capAtFirst := firstCloseIdx + len("</rule>")

	got, err := prompt.RulesAssembler{Src: src, MaxBytes: capAtFirst}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one message, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "rule one body") {
		t.Errorf("first rule missing")
	}
	if strings.Contains(got[0].Text, "rule two body") {
		t.Errorf("second rule should be dropped by byte cap:\n%s", got[0].Text)
	}
	if !strings.Contains(got[0].Text, "more rule(s) not shown") {
		t.Errorf("missing dropped-footer when byte cap truncates:\n%s", got[0].Text)
	}
}

func TestRulesAssemblerCountCap(t *testing.T) {
	rules := []prompt.Rule{
		{Name: "a", Body: "body a\n", Origin: prompt.RuleOriginProject},
		{Name: "b", Body: "body b\n", Origin: prompt.RuleOriginUser},
		{Name: "c", Body: "body c\n", Origin: prompt.RuleOriginDriver},
	}
	src := fakeRulesSource{rules: rules}

	got, err := prompt.RulesAssembler{Src: src, MaxCount: 2}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one message, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "body a") {
		t.Errorf("first rule missing")
	}
	if !strings.Contains(got[0].Text, "body b") {
		t.Errorf("second rule missing")
	}
	if strings.Contains(got[0].Text, "body c") {
		t.Errorf("third rule should be dropped by count cap")
	}
	if !strings.Contains(got[0].Text, "more rule(s) not shown") {
		t.Errorf("missing dropped-footer when count cap truncates:\n%s", got[0].Text)
	}
}

// TestRulesAssemblerRecognisedByIsInjectedTurn0Fragment proves the rendered rules
// message is recognised by the turn-0 fragment predicate, and a genuine user
// instruction is not.
func TestRulesAssemblerRecognisedByIsInjectedTurn0Fragment(t *testing.T) {
	src := fakeRulesSource{rules: []prompt.Rule{
		{Name: "rule1", Body: "body\n", Origin: prompt.RuleOriginProject},
	}}
	got, err := prompt.RulesAssembler{Src: src}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one message, got %d", len(got))
	}
	if !prompt.IsInjectedTurn0Fragment(got[0].Text) {
		t.Fatalf("IsInjectedTurn0Fragment did not recognise the rules fragment:\n%s", got[0].Text)
	}

	// Negatives: a genuine user instruction must NOT be recognised.
	for _, neg := range []string{
		"rename Foo to Bar across the package",
		"",
		"Project rules are unclear, please clarify.",
	} {
		if prompt.IsInjectedTurn0Fragment(neg) {
			t.Fatalf("IsInjectedTurn0Fragment falsely flagged a genuine user turn: %q", neg)
		}
	}
}
