package port

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestLLMRequestStaysProviderNeutral is a DTO-neutrality TRIPWIRE (multi-provider
// Finding B): it asserts LLMRequest carries EXACTLY the four provider-neutral fields
// {System, Messages, Tools, Model}. A new field — especially a provider-specific one
// like a reasoning-effort or thinking-budget — would fail this test on purpose. Such
// provider-PRIVATE knobs belong on the ADAPTER (an openai.WithBaseURL-style Option),
// never on this domain-facing request, because the agent loop must never branch on
// provider. Widening the struct is a deliberate decision that must update this guard.
func TestLLMRequestStaysProviderNeutral(t *testing.T) {
	want := []string{"Messages", "Model", "System", "Tools"}

	typ := reflect.TypeOf(LLMRequest{})
	got := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		got = append(got, typ.Field(i).Name)
	}
	sort.Strings(got)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LLMRequest fields = %v, want %v.\n"+
			"If you added a field: is it provider-NEUTRAL? Provider-private knobs "+
			"(reasoning-effort, thinking-budget, store/include flags) belong on the "+
			"ADAPTER, not LLMRequest. Update this guard only with a deliberate decision.",
			got, want)
	}
}

// TestMessageReasoningCarriersAreNeutral pins the provider-neutral reasoning
// REPLAY carriers on session.Message. The discipline (multi-provider Finding C,
// same posture as Message.Reasoning and Message.ProviderPhase) is: each carrier
// is a single opaque STRING field per message, replayed back verbatim, never
// interpreted/validated/branched on by the harness, never widened into a
// provider-private shape. This reflection guard tripwires a silent widening —
// adding a new reasoning carrier (like ReasoningItemID) is a deliberate decision
// that must add it to the allowed set, and adding a NON-string carrier (a
// provider-private struct/enum) fails the element-type check.
func TestMessageReasoningCarriersAreNeutral(t *testing.T) {
	// The known set of opaque reasoning replay carriers on session.Message.
	// Text is excluded (it is the message body, not a replay blob). Reasoning
	// is the encrypted_content/(thinking,signature) blob; ProviderPhase is the
	// OpenAI Responses phase marker; ReasoningItemID is the reasoning item's
	// provider id. All three are opaque strings replayed verbatim.
	want := []string{"ProviderPhase", "Reasoning", "ReasoningItemID"}

	typ := reflect.TypeOf(session.Message{})
	got := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		f := typ.Field(i)
		// Collect only the bare-string fields that are NOT the message body and
		// NOT a slice/pointer. A reasoning carrier widened into a struct or a
		// provider-private enum would fall outside this filter and shrink the
		// set, failing the equality check below.
		if f.Type.Kind() != reflect.String {
			continue
		}
		if f.Name == "Text" || f.Name == "Role" {
			continue
		}
		// UserPromptProvenance is a domain authority enum, not an opaque
		// provider reasoning carrier; keep its exclusion deliberate so this
		// tripwire continues to catch newly added bare-string carriers.
		if f.Name == "UserPromptProvenance" {
			continue
		}
		got = append(got, f.Name)
	}
	sort.Strings(got)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session.Message opaque reasoning carriers = %v, want %v.\n"+
			"If you added a reasoning carrier: is it a single opaque STRING field, "+
			"replayed verbatim and never interpreted/branched on? Provider-private "+
			"shapes (a struct, an enum, a validated id pattern) violate the neutral "+
			"carrier discipline — keep it a bare string and add it to the want set "+
			"with a deliberate decision.",
			got, want)
	}
}

// TestReasoningItemIDCarrierDiscipline is the doc-contract pin for the
// ReasoningItemID carrier: it asserts the field's doc comment carries the SAME
// discipline vocabulary the established carriers (Reasoning, ProviderPhase) use
// — "opaque", "verbatim", "provider-private", and a "Same discipline as"
// cross-reference — at BOTH declaration sites (the streaming port.Chunk in
// engine/port/llm.go AND the replayed session.Message in engine/session/
// conversation.go), so a future edit that drops the discipline wording (e.g.
// introducing a validation rule or a provider branch on the id) at either site
// fails the gate. It mirrors the existing parser-based tripwire in
// diagnostics_imports_test.go (same package, same mechanism) and the
// established carrier posture exactly.
func TestReasoningItemIDCarrierDiscipline(t *testing.T) {
	sites := []struct {
		path, typ string
	}{
		{"llm.go", "Chunk"},
		{"../session/conversation.go", "Message"},
	}
	for _, site := range sites {
		doc, found := fieldDoc(t, site.path, site.typ, "ReasoningItemID")
		if !found {
			t.Errorf("%s.%s.ReasoningItemID missing — neutral carrier dropped", site.path, site.typ)
			continue
		}
		for _, want := range []string{"opaque", "verbatim", "provider-private", "Same discipline as"} {
			if !strings.Contains(doc, want) {
				t.Errorf("%s.%s.ReasoningItemID doc comment missing discipline term %q.\nThe carrier must stay opaque, replayed verbatim, provider-private, and never interpreted — same posture as Reasoning/ProviderPhase. Got:\n%s", site.path, site.typ, want, doc)
			}
		}
	}
}

// fieldDoc parses file path and returns the doc comment text of field fieldName
// on struct type typeName, plus whether the field was found.
func fieldDoc(t *testing.T, path, typeName, fieldName string) (string, bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != typeName {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if name.Name == fieldName {
						if field.Doc != nil {
							return field.Doc.Text(), true
						}
						return "", true
					}
				}
			}
		}
	}
	return "", false
}
